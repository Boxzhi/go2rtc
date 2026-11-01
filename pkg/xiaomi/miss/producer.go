package miss

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

type Producer struct {
	core.Connection
	client *Client
}

func Dial(rawURL string) (core.Producer, error) {
	client, err := NewClient(rawURL)
	if err != nil {
		return nil, err
	}

	u, _ := url.Parse(rawURL)
	query := u.Query()

	err = client.StartMedia(query.Get("channel"), query.Get("subtype"), query.Get("audio"))
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	quality := query.Get("subtype")
	// The HLC7 HD stream may send VPS/SPS/PPS across separate packets.
	// Keep the original probe for SD and other Xiaomi camera models.
	preferHD := query.Get("model") == "isa.camera.hlc7" && quality != "1" && quality != "sd"
	medias, err := probe(client, query.Get("audio") != "0", preferHD)
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Producer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "xiaomi/miss",
			Protocol:   client.Protocol(),
			RemoteAddr: client.RemoteAddr().String(),
			UserAgent:  client.Version(),
			Medias:     medias,
			Transport:  client,
		},
		client: client,
	}, nil
}

func probe(client *Client, audio bool, preferHD bool) ([]*core.Media, error) {
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	var vcodec, acodec *core.Codec
	var sdFallback *core.Codec
	var sdSince time.Time
	var lastWidth, lastHeight uint16
	var vps, sps, pps []byte

	for {
		pkt, err := client.ReadPacket()
		if err != nil {
			if preferHD && vcodec == nil && sdFallback != nil && (acodec != nil || !audio) {
				vcodec = sdFallback
				fmt.Println("[hlc7-probe] input ended; using original SD parameter sets")
				break
			}
			if vcodec != nil {
				err = fmt.Errorf("no audio")
			} else if acodec != nil {
				err = fmt.Errorf("no video")
			}
			return nil, fmt.Errorf("xiaomi: probe: %w", err)
		}

		switch pkt.CodecID {
		case codecH264:
			if vcodec == nil {
				buf := annexb.EncodeToAVCC(pkt.Payload)
				if h264.NALUType(buf) == h264.NALUTypeSPS {
					vcodec = h264.AVCCToCodec(buf)
				}
			}
		case codecH265:
			if vcodec == nil {
				buf := annexb.EncodeToAVCC(pkt.Payload)
				if preferHD {
					vps, sps, pps = hlc7ReadAVCCParams(buf, vps, sps, pps)
					if len(vps) > 0 && len(sps) > 0 && len(pps) > 0 {
						parsed := h265.DecodeSPS(sps)
						if parsed != nil && parsed.Width() > 0 && parsed.Height() > 0 {
							w, h := parsed.Width(), parsed.Height()
							candidate := h265.AVCCToCodec(hlc7JoinAVCC(vps, sps, pps))
							if w != lastWidth || h != lastHeight {
								fmt.Printf("[hlc7-probe] received HEVC SPS coded size %dx%d\n", w, h)
								lastWidth, lastHeight = w, h
							}
							if w >= 1280 && h >= 720 {
								vcodec = candidate
								fmt.Printf("[hlc7-probe] selected HD parameter sets %dx%d\n", w, h)
							} else if sdFallback == nil {
								sdFallback = candidate
								sdSince = time.Now()
							}
						}
					}
				} else if h265.NALUType(buf) == h265.NALUTypeVPS {
					vcodec = h265.AVCCToCodec(buf)
				}
			}
		case codecPCMA:
			if acodec == nil {
				acodec = &core.Codec{Name: core.CodecPCMA, ClockRate: pkt.SampleRate()}
			}
		case codecOPUS:
			if acodec == nil {
				acodec = &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2}
			}
		}

		if vcodec != nil && (acodec != nil || !audio) {
			break
		}
		if preferHD && vcodec == nil && sdFallback != nil && time.Since(sdSince) >= 4*time.Second && (acodec != nil || !audio) {
			vcodec = sdFallback
			fmt.Println("[hlc7-probe] HD parameter sets not found within 4 seconds; using original SD parameter sets")
			break
		}
	}

	_ = client.SetDeadline(time.Time{})

	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{vcodec},
		},
	}

	if acodec != nil {
		medias = append(medias, &core.Media{
			Kind:      core.KindAudio,
			Direction: core.DirectionRecvonly,
			Codecs:    []*core.Codec{acodec},
		})

		medias = append(medias, &core.Media{
			Kind:      core.KindAudio,
			Direction: core.DirectionSendonly,
			Codecs:    []*core.Codec{acodec.Clone()},
		})
	}

	return medias, nil
}

// hlc7ReadAVCCParams extracts VPS/SPS/PPS from length-prefixed HEVC access
// units. A changed VPS invalidates the collected SPS/PPS; a changed SPS
// invalidates the PPS so a new generation is not assembled from stale data.
func hlc7ReadAVCCParams(buf, vps, sps, pps []byte) ([]byte, []byte, []byte) {
	for len(buf) >= 6 {
		size := int(binary.BigEndian.Uint32(buf[:4]))
		if size < 2 || size > len(buf)-4 {
			break
		}
		nalu := buf[4 : 4+size]
		switch (nalu[0] >> 1) & 0x3f {
		case 32: // VPS
			if !bytes.Equal(vps, nalu) {
				vps = append([]byte(nil), nalu...)
				sps, pps = nil, nil
			}
		case 33: // SPS
			if !bytes.Equal(sps, nalu) {
				sps = append([]byte(nil), nalu...)
				pps = nil
			}
		case 34: // PPS
			if !bytes.Equal(pps, nalu) {
				pps = append([]byte(nil), nalu...)
			}
		}
		buf = buf[4+size:]
	}
	return vps, sps, pps
}

func hlc7JoinAVCC(params ...[]byte) []byte {
	var out []byte
	for _, p := range params {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(p)))
		out = append(out, header[:]...)
		out = append(out, p...)
	}
	return out
}

const timestamp40ms = 48000 * 0.040

func (p *Producer) Start() error {
	var audioTS uint32

	for {
		_ = p.client.SetDeadline(time.Now().Add(10 * time.Second))
		pkt, err := p.client.ReadPacket()
		if err != nil {
			return err
		}

		p.Recv += len(pkt.Payload)

		// TODO: rewrite this
		var name string
		var pkt2 *core.Packet

		switch pkt.CodecID {
		case codecH264, codecH265:
			pkt2 = &core.Packet{
				Header: rtp.Header{
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      TimeToRTP(pkt.Timestamp, 90000),
				},
				Payload: annexb.EncodeToAVCC(pkt.Payload),
			}
			if pkt.CodecID == codecH264 {
				name = core.CodecH264
			} else {
				name = core.CodecH265
			}
		case codecPCMA:
			name = core.CodecPCMA
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			audioTS += uint32(len(pkt.Payload))
		case codecOPUS:
			name = core.CodecOpus
			pkt2 = &core.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         true,
					SequenceNumber: uint16(pkt.Sequence),
					Timestamp:      audioTS,
				},
				Payload: pkt.Payload,
			}
			// known cameras sends packets with 40ms long
			audioTS += timestamp40ms
		}

		for _, recv := range p.Receivers {
			if recv.Codec.Name == name {
				recv.WriteRTP(pkt2)
				break
			}
		}
	}
}

func (p *Producer) Stop() error {
	_ = p.client.StopMedia()
	return p.Connection.Stop()
}

// TimeToRTP convert time in milliseconds to RTP time
func TimeToRTP(timeMS, clockRate uint64) uint32 {
	return uint32(timeMS * clockRate / 1000)
}
