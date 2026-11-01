package miss

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/AlexxIT/go2rtc/pkg/xiaomi/crypto"
)

func hlc7TestAVCC(nalu []byte) []byte {
	packet := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(packet, uint32(len(nalu)))
	copy(packet[4:], nalu)
	return packet
}

func hlc7TestAnnexB(nalu []byte) []byte {
	return append([]byte{0, 0, 0, 1}, nalu...)
}

func hlc7SampleParameterSets(t *testing.T) (vps, sps, pps []byte) {
	t.Helper()
	var err error
	vps, err = base64.StdEncoding.DecodeString("QAEMAf//AWAAAAMAAAMAAAMAAAMAlqwJ")
	if err != nil { t.Fatal(err) }
	sps, err = base64.StdEncoding.DecodeString("QgEBAWAAAAMAAAMAAAMAAAMAlqABciAGifJja5JMmuWcAgAAB9IAAJxoEA==")
	if err != nil { t.Fatal(err) }
	pps, err = base64.StdEncoding.DecodeString("RAHgdrAmQA==")
	if err != nil { t.Fatal(err) }
	return
}

func TestHLC7ReadAVCCParamsCollectsSplitRealCameraParameterSets(t *testing.T) {
	vpsWant, spsWant, ppsWant := hlc7SampleParameterSets(t)
	var vps, sps, pps []byte
	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(vpsWant), vps, sps, pps)
	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(spsWant), vps, sps, pps)
	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(ppsWant), vps, sps, pps)
	if !bytes.Equal(vps, vpsWant) || !bytes.Equal(sps, spsWant) || !bytes.Equal(pps, ppsWant) {
		t.Fatalf("split parameter sets did not round-trip: VPS=%x SPS=%x PPS=%x", vps, sps, pps)
	}
	parsed := h265.DecodeSPS(sps)
	if parsed == nil { t.Fatal("uploaded HLC7 sample SPS did not decode") }
	if parsed.Width() != 2960 || parsed.Height() != 1672 {
		t.Fatalf("unexpected coded SPS size: %dx%d, want 2960x1672", parsed.Width(), parsed.Height())
	}
}

func TestHLC7ReadAVCCParamsDoesNotMixParameterSetGenerations(t *testing.T) {
	vps0, sps0, pps0 := hlc7SampleParameterSets(t)
	vps, sps, pps := hlc7ReadAVCCParams(hlc7TestAVCC(vps0, sps0, pps0), nil, nil, nil)
	if len(vps) == 0 || len(sps) == 0 || len(pps) == 0 { t.Fatal("initial parameter-set generation incomplete") }

	vps1 := append([]byte(nil), vps0...)
	vps1[len(vps1)-1] ^= 1
	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(vps1), vps, sps, pps)
	if !bytes.Equal(vps, vps1) || len(sps) != 0 || len(pps) != 0 {
		t.Fatal("changed VPS must invalidate previously collected SPS/PPS")
	}

	sps1 := append([]byte(nil), sps0...)
	sps1[len(sps1)-1] ^= 1
	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(sps1), vps, sps, pps)
	if !bytes.Equal(sps, sps1) || len(pps) != 0 {
		t.Fatal("changed SPS must invalidate previously collected PPS")
	}

	vps, sps, pps = hlc7ReadAVCCParams(hlc7TestAVCC(pps0), vps, sps, pps)
	if !bytes.Equal(vps, vps1) || !bytes.Equal(sps, sps1) || !bytes.Equal(pps, pps0) {
		t.Fatal("new parameter-set generation was not collected consistently")
	}
}

func TestHLC7JoinAVCCRoundTripsParameterSets(t *testing.T) {
	vps, sps, pps := hlc7SampleParameterSets(t)
	joined := hlc7JoinAVCC(vps, sps, pps)
	codec := h265.AVCCToCodec(joined)
	gotVPS, gotSPS, gotPPS := h265.GetParameterSet(codec.FmtpLine)
	if !bytes.Equal(gotVPS, vps) || !bytes.Equal(gotSPS, sps) || !bytes.Equal(gotPPS, pps) {
		t.Fatal("HLC7 AVCC parameter-set join did not round-trip")
	}
}

func TestHLC7ReadAVCCParamsIgnoresMalformedAndNonParameterNALUs(t *testing.T) {
	nonParameter := hlc7TestAVCC([]byte{0x02, 0x01, 0xaa})
	malformed := []byte{0, 0, 0, 1, 0x40}
	vps, sps, pps := hlc7ReadAVCCParams(nonParameter, nil, nil, nil)
	vps, sps, pps = hlc7ReadAVCCParams(malformed, vps, sps, pps)
	if len(vps)+len(sps)+len(pps) != 0 {
		t.Fatalf("unexpected parameter sets: VPS=%x SPS=%x PPS=%x", vps, sps, pps)
	}
}

func TestHLC7SDFallbackSPSResolutionCanBeDecoded(t *testing.T) {
	sps, err := base64.StdEncoding.DecodeString("QgEBIUAAAAMAkAAAAwAAAwCWoAUCAWlnpbkShc1AQIC4QAAAAwBAAAAFFEn/eEAOpgAV+V8IBBA=")
	if err != nil { t.Fatal(err) }
	parsed := h265.DecodeSPS(sps)
	if parsed == nil { t.Fatal("known-good SD SPS did not decode") }
	if parsed.Width() != 640 || parsed.Height() != 360 {
		t.Fatalf("unexpected SD SPS resolution: %dx%d", parsed.Width(), parsed.Height())
	}
}

type hlc7ProbePacket struct {
	codecID uint32
	payload []byte
}

type hlc7ProbeConn struct {
	key     []byte
	packets []hlc7ProbePacket
	index   int
}

func (c *hlc7ProbeConn) Protocol() string { return "test" }
func (c *hlc7ProbeConn) Version() string { return "test" }
func (c *hlc7ProbeConn) ReadCommand() (uint32, []byte, error) { return 0, nil, io.EOF }
func (c *hlc7ProbeConn) WriteCommand(uint32, []byte) error { return nil }
func (c *hlc7ProbeConn) WritePacket([]byte, []byte) error { return nil }
func (c *hlc7ProbeConn) RemoteAddr() net.Addr { return hlc7TestAddr("probe") }
func (c *hlc7ProbeConn) SetDeadline(time.Time) error { return nil }
func (c *hlc7ProbeConn) Close() error { return nil }
func (c *hlc7ProbeConn) ReadPacket() ([]byte, []byte, error) {
	if c.index >= len(c.packets) { return nil, nil, io.EOF }
	packet := c.packets[c.index]
	c.index++
	header := make([]byte, hdrSize)
	binary.LittleEndian.PutUint32(header[4:], packet.codecID)
	payload, err := crypto.Encode(packet.payload, c.key)
	return header, payload, err
}

type hlc7TestAddr string
func (a hlc7TestAddr) Network() string { return "test" }
func (a hlc7TestAddr) String() string { return string(a) }

func TestProbePrefersHDAfterSDParameterSets(t *testing.T) {
	vps, hdSPS, pps := hlc7SampleParameterSets(t)
	sdSPS, err := base64.StdEncoding.DecodeString("QgEBIUAAAAMAkAAAAwAAAwCWoAUCAWlnpbkShc1AQIC4QAAAAwBAAAAFFEn/eEAOpgAV+V8IBBA=")
	if err != nil { t.Fatal(err) }

	key := bytes.Repeat([]byte{0x5A}, 32)
	conn := &hlc7ProbeConn{key: key, packets: []hlc7ProbePacket{
		{codecH265, hlc7TestAnnexB(vps)},
		{codecH265, hlc7TestAnnexB(sdSPS)},
		{codecH265, hlc7TestAnnexB(pps)},
		{codecH265, hlc7TestAnnexB(vps)},
		{codecH265, hlc7TestAnnexB(hdSPS)},
		{codecH265, hlc7TestAnnexB(pps)},
	}}
	client := &Client{Conn: conn, key: key, model: "isa.camera.hlc7"}
	medias, err := probe(client, false, true)
	if err != nil { t.Fatal(err) }
	if len(medias) == 0 || len(medias[0].Codecs) == 0 { t.Fatal("probe returned no video codec") }
	gotVPS, gotSPS, gotPPS := h265.GetParameterSet(medias[0].Codecs[0].FmtpLine)
	if !bytes.Equal(gotVPS, vps) || !bytes.Equal(gotSPS, hdSPS) || !bytes.Equal(gotPPS, pps) {
		t.Fatal("probe did not select the complete HD parameter-set triplet")
	}
}

type hlc7CommandConn struct {
	Conn
	commands chan []byte
	done chan struct{}
	count int
}
func (c *hlc7CommandConn) ReadCommand() (uint32, []byte, error) {
	data, ok := <-c.commands
	if !ok { close(c.done); return 0, nil, io.EOF }
	c.count++
	return 0, data, nil
}

func TestStartCommandLoopDrainsCommands(t *testing.T) {
	conn := &hlc7CommandConn{commands: make(chan []byte, 3), done: make(chan struct{})}
	client := &Client{Conn: conn}
	client.startCommandLoop()
	conn.commands <- []byte("one")
	conn.commands <- []byte("two")
	conn.commands <- []byte("three")
	close(conn.commands)
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("command reader did not stop after the command stream closed")
	}
	if conn.count != 3 { t.Fatalf("drained %d commands, want 3", conn.count) }
}
