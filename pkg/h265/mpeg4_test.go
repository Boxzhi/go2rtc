package h265

import (
	"encoding/hex"
	"testing"
)

func testParameterSets(t *testing.T) (vps, sps, pps []byte) {
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

func assertArrayCompleteness(t *testing.T, conf []byte, complete bool) {
	t.Helper()
	offset := 23
	for _, naluType := range []byte{NALUTypeVPS, NALUTypeSPS, NALUTypePPS} {
		if len(conf)-offset < 5 { t.Fatalf("truncated hvcC array at %d", offset) }
		header := conf[offset]
		if got := header&0x80 != 0; got != complete { t.Fatalf("array completeness for NALU %d = %v, want %v", naluType, got, complete) }
		if got := header&0x3F; got != naluType { t.Fatalf("array NALU type = %d, want %d", got, naluType) }
		count := int(conf[offset+1])<<8 | int(conf[offset+2])
		if count != 1 { t.Fatalf("array NALU count = %d, want 1", count) }
		size := int(conf[offset+3])<<8 | int(conf[offset+4])
		if size < 2 || offset+5+size > len(conf) { t.Fatalf("invalid NALU size %d", size) }
		offset += 5+size
	}
	if offset != len(conf) { t.Fatalf("unexpected trailing hvcC bytes: %d", len(conf)-offset) }
}

func TestEncodeConfigHVC1MarksParameterSetArraysComplete(t *testing.T) {
	vps, sps, pps := testParameterSets(t)
	assertArrayCompleteness(t, EncodeConfigHVC1(vps, sps, pps), true)
}

func TestEncodeConfigKeepsHEV1ParameterSetArraysIncomplete(t *testing.T) {
	vps, sps, pps := testParameterSets(t)
	assertArrayCompleteness(t, EncodeConfig(vps, sps, pps), false)
}
