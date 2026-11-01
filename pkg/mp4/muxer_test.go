package mp4

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/AlexxIT/go2rtc/pkg/iso"
	"github.com/pion/rtp"
)

func sampleH265ParameterSets(t *testing.T) (vps, sps, pps []byte) {
	t.Helper()
	var err error
	vps, err = hex.DecodeString("40010c01ffff01600000030000030000030000030096ac09")
	if err != nil { t.Fatal(err) }
	sps, err = hex.DecodeString("42010101600000030000030000030000030096a00172200689f2636b924c9ae59c02000007d200009c6810")
	if err != nil { t.Fatal(err) }
	pps, err = hex.DecodeString("4401e076b02640")
	if err != nil { t.Fatal(err) }
	return
}

func newH265Codec(vps, sps, pps []byte) *core.Codec {
	fmtp := fmt.Sprintf("profile-id=1;sprop-vps=%s;sprop-sps=%s;sprop-pps=%s;",
		base64.StdEncoding.EncodeToString(vps), base64.StdEncoding.EncodeToString(sps), base64.StdEncoding.EncodeToString(pps))
	return &core.Codec{Name: core.CodecH265, ClockRate: 90000, FmtpLine: fmtp}
}

func findVideoEntry(t *testing.T, data []byte) *iso.AtomVideo {
	t.Helper()
	atoms, err := iso.DecodeAtoms(data)
	if err != nil { t.Fatal(err) }
	for _, atom := range atoms { if video, ok := atom.(*iso.AtomVideo); ok { return video } }
	t.Fatal("video sample entry not found")
	return nil
}

func findMediaData(t *testing.T, data []byte) []byte {
	t.Helper()
	atoms, err := iso.DecodeAtoms(data)
	if err != nil { t.Fatal(err) }
	for _, atom := range atoms { if media, ok := atom.(*iso.AtomMdat); ok { return media.Data } }
	t.Fatal("mdat atom not found")
	return nil
}

func appendAVCCNALUs(nalus ...[]byte) []byte {
	var out []byte
	for _, nalu := range nalus {
		size := uint32(len(nalu))
		out = append(out, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
		out = append(out, nalu...)
	}
	return out
}

func TestMuxerUsesHVC1WithCompleteParameterSets(t *testing.T) {
	vps, sps, pps := sampleH265ParameterSets(t)
	muxer := &Muxer{}
	muxer.AddTrack(newH265Codec(vps, sps, pps))
	init, err := muxer.GetInit()
	if err != nil { t.Fatal(err) }
	video := findVideoEntry(t, init)
	if video.Name != "hvc1" { t.Fatalf("sample entry = %q, want hvc1", video.Name) }
	_, gotVPS, gotSPS, gotPPS := h265.DecodeConfig(video.Config)
	if !bytes.Equal(gotVPS, vps) || !bytes.Equal(gotSPS, sps) || !bytes.Equal(gotPPS, pps) { t.Fatal("hvcC parameter sets differ from codec configuration") }
	if video.Config[23]&0x80 == 0 || video.Config[23+5+len(vps)]&0x80 == 0 { t.Fatal("hvcC VPS/SPS arrays must be complete") }
	ppsOffset := 23+5+len(vps)+5+len(sps)
	if video.Config[ppsOffset]&0x80 == 0 { t.Fatal("hvcC PPS array must be complete") }
}

func TestMuxerKeepsHev1WhenParameterSetsAreMissing(t *testing.T) {
	muxer := &Muxer{}
	muxer.AddTrack(&core.Codec{Name: core.CodecH265, ClockRate: 90000})
	init, err := muxer.GetInit()
	if err != nil { t.Fatal(err) }
	video := findVideoEntry(t, init)
	if video.Name != "hev1" { t.Fatalf("sample entry = %q, want legacy hev1 fallback", video.Name) }
	if video.Config[23]&0x80 != 0 { t.Fatal("hev1 fallback must not mark parameter-set arrays complete") }
}

func TestMuxerStripsMatchingParameterSetsFromHVC1Samples(t *testing.T) {
	vps, sps, pps := sampleH265ParameterSets(t)
	muxer := &Muxer{}
	muxer.AddTrack(newH265Codec(vps, sps, pps))
	idr := []byte{0x26, 0x01, 0xAA, 0xBB}
	sample := appendAVCCNALUs(vps, sps, pps, idr)
	fragment := muxer.GetPayload(0, &rtp.Packet{Header: rtp.Header{Timestamp: 90000}, Payload: sample})
	if len(fragment) == 0 { t.Fatal("expected a media fragment") }
	got := findMediaData(t, fragment)
	want := appendAVCCNALUs(idr)
	if !bytes.Equal(got, want) { t.Fatalf("hvc1 sample not correctly filtered: got %x, want %x", got, want) }
}

func TestMuxerDropsChangedHVC1ParameterSetsUntilReinitialized(t *testing.T) {
	vps, sps, pps := sampleH265ParameterSets(t)
	muxer := &Muxer{}
	muxer.AddTrack(newH265Codec(vps, sps, pps))
	changedVPS := append([]byte(nil), vps...)
	changedVPS[len(changedVPS)-1] ^= 1
	idr := []byte{0x26, 0x01, 0xAA}
	fragment := muxer.GetPayload(0, &rtp.Packet{Header: rtp.Header{Timestamp: 90000}, Payload: appendAVCCNALUs(changedVPS, idr)})
	if len(fragment) != 0 { t.Fatal("changed parameter sets must not be emitted under the old hvcC") }
	if !muxer.h265Configs[0].invalid { t.Fatal("track must be marked invalid after parameter-set change") }
	fragment = muxer.GetPayload(0, &rtp.Packet{Header: rtp.Header{Timestamp: 180000}, Payload: appendAVCCNALUs(idr)})
	if len(fragment) != 0 { t.Fatal("samples must remain blocked until track reinitialization") }
}

func TestFilterH265ParameterSetsRejectsMalformedAVCC(t *testing.T) {
	vps, sps, pps := sampleH265ParameterSets(t)
	malformed := [][]byte{{0, 0, 0}, {0, 0, 0, 1, 0x40}, {0, 0, 0, 8, 0x40, 0x01}}
	for i, sample := range malformed {
		if _, valid, changed := filterH265ParameterSets(sample, vps, sps, pps); valid || changed {
			t.Fatalf("malformed sample %d should be rejected, got valid=%v changed=%v", i, valid, changed)
		}
	}
}

func TestFilterH265ParameterSetsPreservesOtherNALUs(t *testing.T) {
	vps, sps, pps := sampleH265ParameterSets(t)
	aud := []byte{0x46, 0x01, 0x10}
	idr := []byte{0x26, 0x01, 0xAA}
	sei := []byte{0x4E, 0x01, 0x20}
	filtered, valid, changed := filterH265ParameterSets(appendAVCCNALUs(aud, vps, sps, pps, idr, sei), vps, sps, pps)
	if !valid || changed { t.Fatalf("expected valid matching config, got valid=%v changed=%v", valid, changed) }
	if want := appendAVCCNALUs(aud, idr, sei); !bytes.Equal(filtered, want) { t.Fatalf("unexpected filtered sample: got %x, want %x", filtered, want) }
}
