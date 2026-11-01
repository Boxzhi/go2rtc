package iso

import (
	"bytes"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func decodeVideoEntry(t *testing.T, sampleEntry string, write func(*Movie)) *AtomVideo {
	t.Helper()
	m := NewMovie(128)
	write(m)
	atom, err := DecodeAtom(m.Bytes())
	if err != nil { t.Fatal(err) }
	video, ok := atom.(*AtomVideo)
	if !ok { t.Fatalf("expected AtomVideo, got %T", atom) }
	if video.Name != sampleEntry { t.Fatalf("expected %q, got %q", sampleEntry, video.Name) }
	if !bytes.Equal(video.Config, []byte("test config")) { t.Fatalf("unexpected config: %q", video.Config) }
	return video
}

func TestWriteVideoH265DefaultsToHev1(t *testing.T) {
	decodeVideoEntry(t, "hev1", func(m *Movie) {
		m.WriteVideo(core.CodecH265, 1920, 1080, []byte("test config"))
	})
}

func TestWriteVideoH265SupportsHvc1(t *testing.T) {
	decodeVideoEntry(t, "hvc1", func(m *Movie) {
		m.WriteVideoWithSampleEntry(core.CodecH265, "hvc1", 1920, 1080, []byte("test config"))
	})
}
