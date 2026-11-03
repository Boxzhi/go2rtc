// Package afterstop implements an opt-in, single-camera recording/download POC.
// It is deliberately disabled by default and must never be enabled on a public
// go2rtc endpoint. Completed MP4 data is kept temporarily on the test server.
package afterstop

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

const (
	routePrefix = "/api/luxe/poc2/"
	maxDuration = 30 * time.Second // POC.2 only; 30-minute support is not implemented.
	maxBytes = 256 << 20
	retention = 15 * time.Minute
	maxCompleted = 4
)

type session struct {
	id string
	token string
	source string
	filename string
	path string
	state string
	message string
	started time.Time
	finished time.Time
	expires time.Time
	duration time.Duration
	bytes int64
	audioCodec string
	videoCodec string
	stop chan struct{}
	stopOnce sync.Once
	stream *streams.Stream
	consumer *mp4.Consumer
}

type manager struct {
	mu sync.Mutex
	secret string
	dir string
	activeID string
	sessions map[string]*session
}

type sessionView struct {
	ID string `json:"id"`
	Source string `json:"source"`
	Filename string `json:"filename"`
	State string `json:"state"`
	Message string `json:"message,omitempty"`
	Bytes int64 `json:"bytes"`
	DurationSeconds int `json:"duration_seconds"`
	VideoCodec string `json:"video_codec,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type startRequest struct {
	Source string `json:"source"`
	DurationSeconds int `json:"duration_seconds"`
}

func Init() {
	var cfg struct {
		Mod struct {
			Enabled bool `yaml:"enabled"`
			Secret string `yaml:"secret"`
		} `yaml:"luxe_after_stop_poc"`
	}
	app.LoadConfig(&cfg)
	log := app.GetLogger("afterstop")
	if !cfg.Mod.Enabled {
		return
	}
	if len(cfg.Mod.Secret) < 24 {
		log.Error().Msg("POC disabled: luxe_after_stop_poc.secret must contain at least 24 characters")
		return
	}
	dir, err := os.MkdirTemp("", "luxe-after-stop-poc2-")
	if err != nil {
		log.Error().Err(err).Msg("POC disabled: cannot create temporary recording directory")
		return
	}
	m := &manager{
		secret: cfg.Mod.Secret,
		dir: dir,
		sessions: make(map[string]*session),
	}
	api.HandleFunc("api/luxe/poc2/", m.handle)
	go m.janitor()
	log.Warn().Str("temp_dir", dir).Msg("POC.2 enabled: single camera, 30 seconds maximum, 256 MiB file limit")
}

func newID() (string, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (m *manager) authorized(r *http.Request) bool {
	provided := strings.TrimSpace(r.Header.Get("X-Luxe-POC-Key"))
	return len(provided) == len(m.secret) && subtle.ConstantTimeCompare([]byte(provided), []byte(m.secret)) == 1
}

func respond(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func reject(w http.ResponseWriter, code int, message string) {
	respond(w, code, map[string]string{"error": message})
}

func (m *manager) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	at := strings.LastIndex(r.URL.Path, routePrefix)
	if at < 0 {
		reject(w, http.StatusNotFound, "unknown POC.2 route")
		return
	}
	route := strings.Trim(strings.TrimPrefix(r.URL.Path[at:], routePrefix), "/")
	parts := strings.Split(route, "/")

	// Native WKDownload cannot attach a custom header. Access to a completed
	// file uses a strong, time-limited per-session capability in the URL.
	if len(parts) == 2 && parts[0] == "download" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		m.download(w, r, parts[1])
		return
	}

	if !m.authorized(r) {
		reject(w, http.StatusUnauthorized, "POC key required")
		return
	}
	switch {
	case route == "start" && r.Method == http.MethodPost:
		m.start(w, r)
	case len(parts) == 2 && parts[0] == "status" && r.Method == http.MethodGet:
		m.status(w, parts[1])
	case len(parts) == 2 && parts[0] == "stop" && r.Method == http.MethodPost:
		m.stop(w, parts[1])
	default:
		reject(w, http.StatusNotFound, "unknown POC.2 route")
	}
}

func (s *session) view() sessionView {
	v := sessionView{
		ID:s.id, Source:s.source, Filename:s.filename, State:s.state,
		Message:s.message, Bytes:s.bytes, DurationSeconds:int(s.duration/time.Second),
		VideoCodec:s.videoCodec, AudioCodec:s.audioCodec,
	}
	if !s.finished.IsZero() {
		finished := s.finished
		v.FinishedAt = &finished
	}
	if s.state == "ready" {
		v.DownloadURL = "api/luxe/poc2/download/" + url.PathEscape(s.id) + "?token=" + url.QueryEscape(s.token)
	}
	return v
}

func (m *manager) pruneLocked(now time.Time) {
	for id, s := range m.sessions {
		if s.state == "recording" || s.state == "stopping" {
			continue
		}
		if !s.expires.IsZero() && !now.Before(s.expires) {
			_ = os.Remove(s.path)
			_ = os.Remove(s.path + ".part")
			delete(m.sessions, id)
		}
	}
}

func (m *manager) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		m.pruneLocked(time.Now())
		m.mu.Unlock()
	}
}

func (m *manager) start(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	defer r.Body.Close()
	var request startRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		reject(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if request.DurationSeconds == 0 {
		request.DurationSeconds = 10
	}
	if request.DurationSeconds < 3 || request.DurationSeconds > 30 {
		reject(w, http.StatusBadRequest, "POC.2 duration must be 3–30 seconds")
		return
	}
	if request.Source == "" || len(request.Source) > 128 || strings.ContainsAny(request.Source, " \t\r\n") {
		reject(w, http.StatusBadRequest, "use an existing named go2rtc stream")
		return
	}
	stream := streams.Get(request.Source)
	if stream == nil {
		reject(w, http.StatusNotFound, "stream not found in isolated go2rtc instance")
		return
	}
	id, err := newID()
	if err != nil { reject(w, 500, "cannot create session ID"); return }
	token, err := newID()
	if err != nil { reject(w, 500, "cannot create download token"); return }

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	if m.activeID != "" {
		reject(w, http.StatusConflict, "POC.2 supports only one active recording")
		return
	}
	count := 0
	for _, s := range m.sessions {
		if s.state == "ready" { count++ }
	}
	if count >= maxCompleted {
		reject(w, http.StatusInsufficientStorage, "too many completed POC files: allow cleanup before recording more")
		return
	}

	path := filepath.Join(m.dir, id + ".mp4")
	file, err := os.OpenFile(path+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		reject(w, http.StatusInsufficientStorage, "cannot create temporary MP4")
		return
	}
	// Use existing go2rtc MP4 consumer, asking for audio even when the live
	// preview is muted. This does NOT transcode Opus to AAC.
	media := mp4.ParseQuery(url.Values{"mp4": {"all"}})
	consumer := mp4.NewConsumer(media)
	consumer.FormatName = "mp4"
	consumer.Protocol = "file"
	if err := stream.AddConsumer(consumer); err != nil {
		_ = file.Close()
		_ = os.Remove(path+".part")
		reject(w, http.StatusBadGateway, "go2rtc could not connect recording consumer: " + err.Error())
		return
	}
	var videoCodec, audioCodec string
	for _, codec := range consumer.Codecs() {
		switch codec.Name {
		case core.CodecH264, core.CodecH265:
			videoCodec = codec.Name
		case core.CodecAAC, core.CodecOpus, core.CodecMP3, core.CodecFLAC:
			audioCodec = codec.Name
		}
	}
	if videoCodec == "" {
		stream.RemoveConsumer(consumer)
		_ = file.Close()
		_ = os.Remove(path+".part")
		reject(w, http.StatusUnsupportedMediaType, "no supported video track")
		return
	}
	now := time.Now()
	s := &session{
		id:id, token:token, source:request.Source, filename:fmt.Sprintf("luxe-poc2-%s-%s.mp4",now.Format("20060102-150405"),id[:8]),
		path:path, state:"recording", started:now,
		duration:time.Duration(request.DurationSeconds)*time.Second,
		audioCodec:audioCodec, videoCodec:videoCodec, stop:make(chan struct{}),
		stream:stream, consumer:consumer,
	}
	m.activeID = id
	m.sessions[id] = s
	go m.record(s, file)
	respond(w, http.StatusAccepted, s.view())
}

type boundedWriter struct {
	out io.Writer
	max int64
	n int64
}
var errTooLarge = errors.New("temporary recording exceeds 256 MiB POC limit")

func (b *boundedWriter) Write(data []byte) (int, error) {
	if b.n + int64(len(data)) > b.max {
		return 0, errTooLarge
	}
	n, err := b.out.Write(data)
	b.n += int64(n)
	return n, err
}

func (m *manager) record(s *session, file *os.File) {
	writer := &boundedWriter{out:file, max:maxBytes}
	writerDone := make(chan struct{})
	consumerStopped := make(chan struct{})
	// Recorder is independent from the HA frontend/WebView lifecycle.
	go func() {
		timer := time.NewTimer(s.duration)
		defer timer.Stop()
		select {
		case <-s.stop:
		case <-timer.C:
		case <-writerDone:
		}
		s.stream.RemoveConsumer(s.consumer)
		close(consumerStopped)
	}()
	_, writeErr := s.consumer.WriteTo(writer)
	close(writerDone)
	<-consumerStopped

	if err := file.Sync(); writeErr == nil { writeErr = err }
	if err := file.Close(); writeErr == nil { writeErr = err }
	size := writer.n
	if writeErr == nil && size < 1024 {
		writeErr = errors.New("recording contains no usable media")
	}
	if writeErr == nil {
		writeErr = os.Rename(s.path+".part",s.path)
	}
	if writeErr != nil {
		_ = os.Remove(s.path+".part")
		_ = os.Remove(s.path)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.bytes = size
	s.finished = time.Now()
	s.expires = s.finished.Add(retention)
	if writeErr != nil {
		s.state = "failed"
		s.message = writeErr.Error()
	} else {
		s.state = "ready"
	}
	if m.activeID == s.id {
		m.activeID = ""
	}
}

func (m *manager) status(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	s := m.sessions[id]
	if s == nil { reject(w, 404, "recording not found or expired"); return }
	respond(w, 200, s.view())
}

func (m *manager) stop(w http.ResponseWriter, id string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		reject(w, http.StatusNotFound, "recording not found")
		return
	}
	if s.state == "recording" {
		s.state = "stopping"
		s.stopOnce.Do(func(){close(s.stop)})
	}
	v := s.view()
	m.mu.Unlock()
	respond(w, 200, v)
}

func (m *manager) download(w http.ResponseWriter, r *http.Request, id string) {
	token := r.URL.Query().Get("token")
	m.mu.Lock()
	m.pruneLocked(time.Now())
	s := m.sessions[id]
	if s == nil || s.state != "ready" || token == "" || len(token) != len(s.token) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		m.mu.Unlock()
		reject(w, http.StatusNotFound, "download unavailable or expired")
		return
	}
	filename, filePath := s.filename, s.path
	m.mu.Unlock()

	file, err := os.Open(filePath)
	if err != nil { reject(w, 404, "temporary MP4 missing"); return }
	defer file.Close()
	info, err := file.Stat()
	if err != nil { reject(w, 500, "could not stat recording"); return }
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

// Do not use as a production recording subsystem.
// This POC is deliberately short-lived, single-stream and test-only.
