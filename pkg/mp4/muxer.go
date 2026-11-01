package mp4

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"log"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/AlexxIT/go2rtc/pkg/iso"
	"github.com/pion/rtp"
)

type h265TrackConfig struct {
	hvc1    bool
	vps     []byte
	sps     []byte
	pps     []byte
	invalid bool
}

type Muxer struct {
	index       uint32
	dts         []uint64
	pts         []uint32
	codecs      []*core.Codec
	h265Configs []h265TrackConfig
}

func isH265ParameterSet(nalu []byte, naluType byte) bool {
	return len(nalu) >= 2 && ((nalu[0]>>1)&0x3F) == naluType
}

func (m *Muxer) AddTrack(codec *core.Codec) {
	m.dts = append(m.dts, 0)
	m.pts = append(m.pts, 0)
	m.codecs = append(m.codecs, codec)

	config := h265TrackConfig{}
	if codec.Name == core.CodecH265 {
		config.vps, config.sps, config.pps = h265.GetParameterSet(codec.FmtpLine)
		config.hvc1 = isH265ParameterSet(config.vps, h265.NALUTypeVPS) &&
			len(config.sps) >= 6 && isH265ParameterSet(config.sps, h265.NALUTypeSPS) &&
			isH265ParameterSet(config.pps, h265.NALUTypePPS)
	}
	m.h265Configs = append(m.h265Configs, config)
}

func (m *Muxer) GetInit() ([]byte, error) {
	mv := iso.NewMovie(1024)
	mv.WriteFileType()

	mv.StartAtom(iso.Moov)
	mv.WriteMovieHeader()

	for i, codec := range m.codecs {
		switch codec.Name {
		case core.CodecH264:
			sps, pps := h264.GetParameterSet(codec.FmtpLine)
			// some dummy SPS and PPS not a problem for MP4, but problem for HLS :(
			if len(sps) == 0 {
				sps = []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}
			}
			if len(pps) == 0 {
				pps = []byte{0x68, 0xce, 0x38, 0x80}
			}

			var width, height uint16
			if s := h264.DecodeSPS(sps); s != nil {
				width = s.Width()
				height = s.Height()
			} else {
				width = 1920
				height = 1080
			}

			mv.WriteVideoTrack(
				uint32(i+1), codec.Name, codec.ClockRate, width, height, h264.EncodeConfig(sps, pps),
			)

		case core.CodecH265:
			trackConfig := m.h265Configs[i]
			vps, sps, pps := trackConfig.vps, trackConfig.sps, trackConfig.pps
			// Keep the legacy hev1 path for streams without complete fmtp parameter sets.
			if len(vps) == 0 {
				vps = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x40, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x99, 0xac, 0x09}
			}
			if len(sps) == 0 {
				sps = []byte{0x42, 0x01, 0x01, 0x01, 0x40, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x99, 0xa0, 0x01, 0x40, 0x20, 0x05, 0xa1, 0xfe, 0x5a, 0xee, 0x46, 0xc1, 0xae, 0x55, 0x04}
			}
			if len(pps) == 0 {
				pps = []byte{0x44, 0x01, 0xc0, 0x73, 0xc0, 0x4c, 0x90}
			}
			var width, height uint16
			if parsed := h265.DecodeSPS(sps); parsed != nil {
				width, height = parsed.Width(), parsed.Height()
			} else {
				width, height = 1920, 1080
			}
			sampleEntry := "hev1"
			conf := h265.EncodeConfig(vps, sps, pps)
			if trackConfig.hvc1 {
				sampleEntry = "hvc1"
				conf = h265.EncodeConfigHVC1(trackConfig.vps, trackConfig.sps, trackConfig.pps)
			}
			mv.WriteVideoTrackWithEntry(uint32(i+1), codec.Name, codec.ClockRate, width, height, conf, sampleEntry)
		case core.CodecAAC:
			s := core.Between(codec.FmtpLine, "config=", ";")
			b, err := hex.DecodeString(s)
			if err != nil {
				return nil, err
			}

			mv.WriteAudioTrack(
				uint32(i+1), codec.Name, codec.ClockRate, uint16(codec.Channels), b,
			)

		case core.CodecOpus, core.CodecMP3, core.CodecPCMA, core.CodecPCMU, core.CodecFLAC:
			mv.WriteAudioTrack(
				uint32(i+1), codec.Name, codec.ClockRate, uint16(codec.Channels), nil,
			)
		}
	}

	mv.StartAtom(iso.MoovMvex)
	for i := range m.codecs {
		mv.WriteTrackExtend(uint32(i + 1))
	}
	mv.EndAtom() // MVEX

	mv.EndAtom() // MOOV

	return mv.Bytes(), nil
}

func (m *Muxer) Reset() {
	m.index = 0
	for i := range m.dts {
		m.dts[i] = 0
		m.pts[i] = 0
	}
}

func (m *Muxer) GetPayload(trackID byte, packet *rtp.Packet) []byte {
	codec := m.codecs[trackID]
	payload := packet.Payload

	if codec.Name == core.CodecH265 {
		config := &m.h265Configs[trackID]
		if config.hvc1 {
			if config.invalid {
				return nil
			}
			filtered, valid, changed := filterH265ParameterSets(payload, config.vps, config.sps, config.pps)
			if !valid {
				if changed {
					config.invalid = true
					log.Printf("[mp4] H265 parameter sets changed; a new initialization segment is required")
				}
				return nil
			}
			payload = filtered
			if len(payload) == 0 {
				return nil
			}
		}
	}

	m.index++
	duration := packet.Timestamp - m.pts[trackID]
	m.pts[trackID] = packet.Timestamp

	var flags uint32
	switch codec.Name {
	case core.CodecH264:
		if h264.IsKeyframe(payload) { flags = iso.SampleVideoIFrame } else { flags = iso.SampleVideoNonIFrame }
	case core.CodecH265:
		if h265.IsKeyframe(payload) { flags = iso.SampleVideoIFrame } else { flags = iso.SampleVideoNonIFrame }
	case core.CodecAAC:
		duration = 1024
		flags = iso.SampleAudio
	default:
		flags = iso.SampleAudio
	}

	if duration == 0 || duration > codec.ClockRate {
		duration = codec.ClockRate/1000 + 1
		m.pts[trackID] += duration
	}
	size := len(payload)
	mv := iso.NewMovie(1024 + size)
	mv.WriteMovieFragment(m.index, uint32(trackID+1), duration, uint32(size), flags, m.dts[trackID], uint32(packet.ExtensionProfile))
	mv.WriteData(payload)
	m.dts[trackID] += uint64(duration)
	return mv.Bytes()
}

// filterH265ParameterSets validates AVCC framing, strips matching in-band
// VPS/SPS/PPS for hvc1, and detects parameter-set changes that require a new
// initialization segment. valid=false, changed=true signals changed config.
func filterH265ParameterSets(avcc, expectedVPS, expectedSPS, expectedPPS []byte) (filtered []byte, valid, changed bool) {
	if len(avcc) == 0 { return nil, false, false }
	hasParameterSets := false
	for offset := 0; offset < len(avcc); {
		if len(avcc)-offset < 4 { return nil, false, false }
		naluSize := uint64(binary.BigEndian.Uint32(avcc[offset:offset+4]))
		if naluSize < 2 || naluSize > uint64(len(avcc)-offset-4) { return nil, false, false }
		size := int(naluSize)
		nalu := avcc[offset+4 : offset+4+size]
		var expected []byte
		switch (nalu[0] >> 1) & 0x3F {
		case h265.NALUTypeVPS: expected = expectedVPS
		case h265.NALUTypeSPS: expected = expectedSPS
		case h265.NALUTypePPS: expected = expectedPPS
		}
		if expected != nil {
			hasParameterSets = true
			if !bytes.Equal(nalu, expected) { return nil, false, true }
		}
		offset += 4 + size
	}
	if !hasParameterSets { return avcc, true, false }

	filtered = make([]byte, 0, len(avcc))
	for offset := 0; offset < len(avcc); {
		size := int(binary.BigEndian.Uint32(avcc[offset:offset+4]))
		nalu := avcc[offset+4 : offset+4+size]
		typ := (nalu[0] >> 1) & 0x3F
		if typ != h265.NALUTypeVPS && typ != h265.NALUTypeSPS && typ != h265.NALUTypePPS {
			filtered = append(filtered, avcc[offset:offset+4+size]...)
		}
		offset += 4 + size
	}
	return filtered, true, false
}
