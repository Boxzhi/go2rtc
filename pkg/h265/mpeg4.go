// Package h265 - MPEG4 format related functions
package h265

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func DecodeConfig(conf []byte) (profile, vps, sps, pps []byte) {
	profile = conf[1:4]

	b := conf[23:]
	if binary.BigEndian.Uint16(b[1:]) != 1 {
		return
	}
	vpsSize := binary.BigEndian.Uint16(b[3:])
	vps = b[5 : 5+vpsSize]

	b = conf[23+5+vpsSize:]
	if binary.BigEndian.Uint16(b[1:]) != 1 {
		return
	}
	spsSize := binary.BigEndian.Uint16(b[3:])
	sps = b[5 : 5+spsSize]

	b = conf[23+5+vpsSize+5+spsSize:]
	if binary.BigEndian.Uint16(b[1:]) != 1 {
		return
	}
	ppsSize := binary.BigEndian.Uint16(b[3:])
	pps = b[5 : 5+ppsSize]

	return
}

func EncodeConfig(vps, sps, pps []byte) []byte {
	return encodeConfig(vps, sps, pps, false)
}

// EncodeConfigHVC1 marks the VPS/SPS/PPS arrays complete. hvc1 samples carry
// parameter sets in hvcC rather than repeating them in media samples.
func EncodeConfigHVC1(vps, sps, pps []byte) []byte {
	return encodeConfig(vps, sps, pps, true)
}

func encodeConfig(vps, sps, pps []byte, arrayComplete bool) []byte {
	vpsSize, spsSize, ppsSize := uint16(len(vps)), uint16(len(sps)), uint16(len(pps))
	buf := make([]byte, 23+5+vpsSize+5+spsSize+5+ppsSize)
	buf[0] = 1
	copy(buf[1:], sps[3:6])
	buf[21] = 3
	buf[22] = 3

	var completeness byte
	if arrayComplete { completeness = 0x80 }

	b := buf[23:]
	b[0] = completeness | ((vps[0] >> 1) & 0x3F)
	binary.BigEndian.PutUint16(b[1:], 1)
	binary.BigEndian.PutUint16(b[3:], vpsSize)
	copy(b[5:], vps)

	b = buf[23+5+vpsSize:]
	b[0] = completeness | ((sps[0] >> 1) & 0x3F)
	binary.BigEndian.PutUint16(b[1:], 1)
	binary.BigEndian.PutUint16(b[3:], spsSize)
	copy(b[5:], sps)

	b = buf[23+5+vpsSize+5+spsSize:]
	b[0] = completeness | ((pps[0] >> 1) & 0x3F)
	binary.BigEndian.PutUint16(b[1:], 1)
	binary.BigEndian.PutUint16(b[3:], ppsSize)
	copy(b[5:], pps)
	return buf
}

func ConfigToCodec(conf []byte) *core.Codec {
	buf := bytes.NewBufferString("profile-id=1")

	_, vps, sps, pps := DecodeConfig(conf)
	if vps != nil {
		buf.WriteString(";sprop-vps=")
		buf.WriteString(base64.StdEncoding.EncodeToString(vps))
	}
	if sps != nil {
		buf.WriteString(";sprop-sps=")
		buf.WriteString(base64.StdEncoding.EncodeToString(sps))
	}
	if pps != nil {
		buf.WriteString(";sprop-pps=")
		buf.WriteString(base64.StdEncoding.EncodeToString(pps))
	}

	return &core.Codec{
		Name:        core.CodecH265,
		ClockRate:   90000,
		FmtpLine:    buf.String(),
		PayloadType: core.PayloadTypeRAW,
	}
}
