package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/netip"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	apiPath        = "/api/luxe/recordings"
	maxSessions    = 3
	maxDuration    = 30 * time.Minute
	pendingTTL     = time.Minute
	finishedTTL    = 15 * time.Minute
	authTimeout    = 3 * time.Second
	maxRequestBody = 8 * 1024
)

const (
	statePending     = "pending"
	stateStarting    = "starting"
	stateRecording   = "recording"
	stateStopping    = "stopping"
	stateStopped     = "stopped"
	stateFailed      = "failed"
	stateInterrupted = "interrupted"
	stateExpired     = "expired"
)

var (
	errMaxSessions = errors.New("maximum number of simultaneous recordings reached")
	errDuplicate   = errors.New("this camera already has an active recording")
	errNotPending  = errors.New("recording session is not waiting for a stream")
)

type haUser struct {
	ID string `json:"id"`
}

type session struct {
	ID         string
	Source     string
	Filename   string
	UserID     string
	State      string
	Error      string
	StopReason string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Stream           *streams.Stream
	Cancel           context.CancelFunc
	DownloadTokenHash [32]byte
	CookiePath        string
	CookieSecure      bool
	StreamClaimed     bool
	BytesSent         atomic.Int64 // HTTP response bytes; not buffered recording data
}

type sessionView struct {
	SessionID  string     `json:"session_id"`
	Source     string     `json:"source"`
	Filename   string     `json:"filename"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	BytesSent  int64      `json:"bytes_sent"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	authURL  string
	client   *http.Client
	now      func() time.Time
}

var log zerolog.Logger

// Init registers the Luxe recording-session API. Recording data is streamed
// directly to the authenticated browser; this package never writes video files
// to the add-on filesystem or Home Assistant /media.
func Init() {
	log = app.GetLogger("recording")

	var cfg struct {
		Mod struct {
			Enabled bool   `yaml:"enabled"`
			AuthURL string `yaml:"auth_url"`
		} `yaml:"luxe_recording"`
	}
	cfg.Mod.Enabled = true
	cfg.Mod.AuthURL = "http://homeassistant:8123/api/auth/current"
	app.LoadConfig(&cfg)
	if !cfg.Mod.Enabled {
		log.Info().Msg("Luxe recording API disabled by configuration")
		return
	}

	m := newManager(cfg.Mod.AuthURL)
	api.HandleFunc("api/luxe/recordings", m.handle)
	api.HandleFunc("api/luxe/recordings/", m.handle)
	log.Info().Msg("Luxe recording-session API enabled")
}

func newManager(authURL string) *manager {
	return &manager{
		sessions: make(map[string]*session),
		authURL:  authURL,
		client: &http.Client{
			Timeout: authTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

func (m *manager) handle(w http.ResponseWriter, r *http.Request) {
	route := recordingRoute(r.URL.Path)

	// The media response is a native browser download. Its short-lived,
	// path-scoped HttpOnly cookie is issued by the authenticated start request,
	// so the browser can navigate to this URL without exposing a HA token in
	// the URL or buffering the MP4 through JavaScript.
	if strings.HasSuffix(route, "/stream") && r.Method == http.MethodGet {
		m.stream(w, r, strings.TrimSuffix(route, "/stream"))
		return
	}

	user, status, err := m.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	switch {
	case route == "" && r.Method == http.MethodGet:
		m.list(w, user)
	case route == "start" && r.Method == http.MethodPost:
		m.start(w, r, user)
	case strings.HasSuffix(route, "/stop") && r.Method == http.MethodPost:
		m.stop(w, user, strings.TrimSuffix(route, "/stop"))
	case !strings.Contains(route, "/") && route != "" && r.Method == http.MethodGet:
		m.status(w, user, route)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method or recording endpoint not allowed")
	}
}

func recordingRoute(requestPath string) string {
	index := strings.LastIndex(requestPath, apiPath)
	if index < 0 {
		return ""
	}
	tail := strings.TrimPrefix(requestPath[index+len(apiPath):], "/")
	return strings.Trim(tail, "/")
}

// isSupervisorIngress trusts only the Home Assistant Supervisor ingress peer,
// not spoofable X-Forwarded-* headers or requests arriving on port 1984 from
// other network clients. Supervisor itself strips and replaces X-Remote-User-Id.
func isSupervisorIngress(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil { return false }
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Unmap() != netip.MustParseAddr("172.30.32.2") { return false }
	path := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("X-Ingress-Path")), "/")
	if !strings.HasPrefix(path, "/api/hassio_ingress/") { return false }
	token := strings.TrimPrefix(path, "/api/hassio_ingress/")
	return token != "" && !strings.ContainsAny(token, "/\\\\.")
}

func (m *manager) authenticate(r *http.Request) (*haUser, int, error) {
	// Native Ingress has already authenticated the user. Do not require the
	// embedded POC page to extract a HA access token from its parent window.
	if isSupervisorIngress(r) {
		if id := strings.TrimSpace(r.Header.Get("X-Remote-User-Id")); id != "" {
			return &haUser{ID: id}, 0, nil
		}
		return nil, http.StatusUnauthorized, errors.New("Ingress session does not identify a Home Assistant user")
	}
	// Non-Ingress callers must still present a HA Bearer token, validated
	// against Home Assistant Core. Never accept X-Remote-User-Id from LAN.
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return nil, http.StatusUnauthorized, errors.New("Home Assistant Bearer token required")
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, m.authURL, nil)
	if err != nil {
		return nil, http.StatusServiceUnavailable, errors.New("Home Assistant authentication endpoint is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, http.StatusServiceUnavailable, errors.New("Home Assistant authentication service is unavailable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, http.StatusUnauthorized, errors.New("Home Assistant access token is invalid or expired")
	}

	var user haUser
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&user); err != nil || user.ID == "" {
		return nil, http.StatusUnauthorized, errors.New("Home Assistant did not return a valid user identity")
	}
	return &user, 0, nil
}

type startRequest struct {
	Source   string `json:"source"`
	Filename string `json:"filename"`
}

func (m *manager) start(w http.ResponseWriter, r *http.Request, user *haUser) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer r.Body.Close()

	var input startRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid recording request")
		return
	}

	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" || len(input.Source) > 128 || strings.ContainsAny(input.Source, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "source must be an existing go2rtc stream name")
		return
	}

	stream := streams.Get(input.Source)
	if stream == nil {
		writeError(w, http.StatusNotFound, "go2rtc stream not found")
		return
	}

	s, err := m.create(input.Source, sanitizeFilename(input.Filename), user.ID, stream)
	if err != nil {
		switch {
		case errors.Is(err, errMaxSessions), errors.Is(err, errDuplicate):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "could not create recording session")
		}
		return
	}

	downloadURL := m.downloadURL(r, s.ID)
	if err = m.setDownloadCookie(w, r, s, downloadURL); err != nil {
		m.discard(s.ID)
		writeError(w, http.StatusInternalServerError, "could not issue a secure download session")
		return
	}

	api.ResponseJSON(w, map[string]any{
		"ok":                   true,
		"session_id":           s.ID,
		"download_url":         downloadURL,
		"filename":             s.Filename,
		"max_duration_seconds": int(maxDuration.Seconds()),
	})
	log.Info().Str("session", s.ID).Str("source", s.Source).Str("user", user.ID).Msg("recording session created")
}

func (m *manager) create(source, filename, userID string, stream *streams.Stream) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.pruneLocked(now)

	active := 0
	for _, current := range m.sessions {
		if isActive(current.State) {
			active++
			if current.Source == source {
				return nil, errDuplicate
			}
		}
	}
	if active >= maxSessions {
		return nil, errMaxSessions
	}

	s := &session{
		ID:        uuid.NewString(),
		Source:    source,
		Filename:  filename,
		UserID:    userID,
		State:     statePending,
		CreatedAt: now,
		Stream:    stream,
	}
	m.sessions[s.ID] = s
	return s, nil
}

func (m *manager) stream(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithCancel(r.Context())
	s, status, err := m.claimDownload(r, id, cancel)
	if err != nil {
		cancel()
		writeError(w, status, err.Error())
		return
	}
	m.clearDownloadCookie(w, s)

	stream := s.Stream
	timer := time.AfterFunc(maxDuration, func() {
		m.requestStop(s.ID, "time_limit")
	})
	defer timer.Stop()

	medias := mp4.ParseQuery(url.Values{"mp4": []string{"all"}})
	consumer := mp4.NewConsumer(medias)
	consumer.FormatName = "mp4"
	consumer.Protocol = "http"
	consumer.Source = s.Source
	consumer.WithRequest(r)

	if err = stream.AddConsumer(consumer); err != nil {
		ctxErr := ctx.Err()
		m.finish(s, err, ctxErr)
		cancel()
		log.Warn().Err(err).Str("source", s.Source).Msg("recording stream could not start")
		writeError(w, http.StatusBadGateway, "go2rtc could not attach to the camera stream: "+err.Error())
		return
	}

	if ctx.Err() != nil {
		stream.RemoveConsumer(consumer)
		m.finish(s, nil, ctx.Err())
		cancel()
		return
	}

	// Keep the session in 'starting' until the first actual media fragment
	// has been written. Successfully attaching a Consumer alone does not
	// prove the camera has delivered a keyframe.
	cleanupDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		// Gracefully stop only when the user pressed Stop or the 30-minute
		// server limit fired. On a browser disconnect, do not wait for a
		// dead peer: release the Consumer immediately.
		if r.Context().Err() == nil {
			m.mu.Lock()
			reason := s.StopReason
			m.mu.Unlock()
			if reason == "user" || reason == "time_limit" {
				drainCtx, cancelDrain := context.WithTimeout(context.Background(), 5*time.Second)
				if err := consumer.Drain(drainCtx); err != nil {
					m.noteStreamFailure(s, "drain_timeout", "could not flush pending MP4 fragments: "+err.Error())
				}
				cancelDrain()
			}
		}
		stream.RemoveConsumer(consumer)
		close(cleanupDone)
	}()

	dropMonitorDone := make(chan struct{})
	go func() {
		defer close(dropMonitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if dropped := consumer.DropCount(); dropped > 0 {
					message := fmt.Sprintf("MP4 transport dropped %d packets; recording stopped to avoid an incomplete file", dropped)
					m.requestFailure(s.ID, "transport_drops", message)
					return
				}
			}
		}
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": s.Filename}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)

	writer := &flushWriter{
		ResponseWriter: w,
		onFirstMedia: func() { m.markMediaStarted(s) },
		onBytes: func(n int) { s.BytesSent.Add(int64(n)) },
	}
	_, writeErr := consumer.WriteTo(writer)
	ctxErr := ctx.Err()
	cancel()
	<-cleanupDone
	<-dropMonitorDone

	// The periodic monitor may miss drops that happened just before Stop.
	// Check the preserved sender counters again after the final drain.
	if dropped := consumer.DropCount(); dropped > 0 && r.Context().Err() == nil {
		m.noteStreamFailure(s, "transport_drops", fmt.Sprintf("recording lost %d MP4 transport packets", dropped))
	}

	// MP4 initialization alone is not a playable camera recording. Report
	// a failed session if normal stop/timeout occurred before any media.
	if !writer.mediaWritten && r.Context().Err() == nil {
		m.noteStreamFailure(s, "no_media", "camera did not deliver a media frame")
	}
	m.finish(s, writeErr, ctxErr)
	if writeErr != nil && ctxErr == nil {
		log.Warn().Err(writeErr).Str("session", s.ID).Msg("recording stream ended with an error")
	} else {
		log.Info().Str("session", s.ID).Str("reason", s.StopReason).Msg("recording stream stopped")
	}
}

func (m *manager) stop(w http.ResponseWriter, user *haUser, id string) {
	if id == "" {
		writeError(w, http.StatusBadRequest, "recording session ID is required")
		return
	}
	s, err := m.getOwned(id, user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	m.requestStop(id, "user")
	m.mu.Lock()
	view := viewOf(s)
	m.mu.Unlock()
	api.ResponseJSON(w, map[string]any{"ok": true, "session": view})
}

func (m *manager) requestStop(id, reason string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return
	}

	var cancel context.CancelFunc
	switch s.State {
	case statePending:
		s.State = stateStopped
		s.StopReason = reason
		s.FinishedAt = m.now()
	case stateStarting, stateRecording:
		s.State = stateStopping
		s.StopReason = reason
		cancel = s.Cancel
	case stateStopping:
		if s.StopReason == "" {
			s.StopReason = reason
		}
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (m *manager) requestFailure(id, reason, message string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return
	}

	var cancel context.CancelFunc
	switch s.State {
	case stateStarting, stateRecording:
		s.State = stateStopping
		s.StopReason = reason
		s.Error = message
		cancel = s.Cancel
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (m *manager) status(w http.ResponseWriter, user *haUser, id string) {
	s, err := m.getOwned(id, user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	m.mu.Lock()
	view := viewOf(s)
	m.mu.Unlock()
	api.ResponseJSON(w, view)
}

func (m *manager) list(w http.ResponseWriter, user *haUser) {
	m.mu.Lock()
	m.pruneLocked(m.now())
	items := make([]sessionView, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.UserID == user.ID {
			items = append(items, viewOf(s))
		}
	}
	m.mu.Unlock()

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	api.ResponseJSON(w, map[string]any{"sessions": items})
}

func (m *manager) getOwned(id, userID string) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now())
	s := m.sessions[id]
	if s == nil || s.UserID != userID {
		return nil, errors.New("recording session not found")
	}
	return s, nil
}

func (m *manager) markMediaStarted(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.StartedAt.IsZero() {
		s.StartedAt = m.now()
	}
	if s.State == stateStarting {
		s.State = stateRecording
	}
}

func (m *manager) noteStreamFailure(s *session, reason, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A detected transport drop takes precedence over subsequent cleanup.
	if s.StopReason != "transport_drops" {
		s.StopReason = reason
		s.Error = message
	}
}

func (m *manager) finish(s *session, writeErr, ctxErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case s.StopReason == "transport_drops" || s.StopReason == "no_media" || s.StopReason == "drain_timeout":
		s.State = stateFailed
		if s.Error == "" {
			s.Error = "recording stopped after transport packet loss"
		}
	case ctxErr != nil && s.StopReason == "":
		s.State = stateInterrupted
		s.StopReason = "client_disconnect"
	case writeErr != nil && ctxErr == nil:
		s.State = stateFailed
		s.Error = writeErr.Error()
	default:
		s.State = stateStopped
	}
	if s.FinishedAt.IsZero() {
		s.FinishedAt = m.now()
	}
	s.Cancel = nil
	s.DownloadTokenHash = [32]byte{}
}

func (m *manager) pruneLocked(now time.Time) {
	for id, s := range m.sessions {
		if s.State == statePending && now.Sub(s.CreatedAt) > pendingTTL {
			s.State = stateExpired
			s.StopReason = "start_timeout"
			s.FinishedAt = now
		}
		if !s.FinishedAt.IsZero() && now.Sub(s.FinishedAt) > finishedTTL {
			delete(m.sessions, id)
		}
	}
}

func (m *manager) downloadURL(r *http.Request, id string) string {
	// Ingress strips its secret URL prefix before forwarding the request and
	// supplies the original prefix in X-Ingress-Path. Preserve an optional
	// go2rtc api.base_path as well.
	index := strings.LastIndex(r.URL.Path, apiPath)
	basePath := ""
	if index >= 0 {
		basePath = strings.TrimRight(r.URL.Path[:index], "/")
	}

	ingressPath := strings.TrimRight(r.Header.Get("X-Ingress-Path"), "/")
	if !strings.HasPrefix(ingressPath, "/api/hassio_ingress/") || strings.Contains(ingressPath, "..") {
		ingressPath = ""
	}
	return ingressPath + basePath + apiPath + "/" + id + "/stream"
}

func sanitizeFilename(filename string) string {
	filename = strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")
	filename = path.Base(filename)
	var b strings.Builder
	for _, r := range filename {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("._- ", r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	result := strings.Trim(b.String(), " ._")
	if result == "" || result == "." {
		result = "luxe-camera"
	}
	runes := []rune(result)
	if len(runes) > 96 {
		result = string(runes[:96])
	}
	if !strings.HasSuffix(strings.ToLower(result), ".mp4") {
		result += ".mp4"
	}
	return result
}

func isActive(state string) bool {
	switch state {
	case statePending, stateStarting, stateRecording, stateStopping:
		return true
	default:
		return false
	}
}

func viewOf(s *session) sessionView {
	view := sessionView{
		SessionID:  s.ID,
		Source:     s.Source,
		Filename:   s.Filename,
		State:      s.State,
		Error:      s.Error,
		StopReason: s.StopReason,
		BytesSent:  s.BytesSent.Load(),
		CreatedAt:  s.CreatedAt,
	}
	if !s.StartedAt.IsZero() {
		started := s.StartedAt
		view.StartedAt = &started
	}
	if !s.FinishedAt.IsZero() {
		finished := s.FinishedAt
		view.FinishedAt = &finished
	}
	return view
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", api.MimeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":    false,
		"error": message,
	})
}

type flushWriter struct {
	http.ResponseWriter
	onFirstMedia func()
	onBytes func(int)
	writes int
	mediaWritten bool
}

func (w *flushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		if w.onBytes != nil { w.onBytes(n) }
		w.writes++
		// The first mp4.Consumer.WriteTo call writes the fMP4 init segment;
		// its subsequent writes carry actual media fragments.
		if w.writes > 1 && !w.mediaWritten {
			w.mediaWritten = true
			if w.onFirstMedia != nil {
				w.onFirstMedia()
			}
		}
	}
	w.Flush()
	return n, err
}

func (w *flushWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
