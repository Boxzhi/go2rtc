package recording

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/google/uuid"
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
	statePending   = "pending"
	stateStarting  = "starting"
	stateRecording = "recording"
	stateStopping  = "stopping"
	stateStopped   = "stopped"
	stateFailed    = "failed"
	stateExpired   = "expired"
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
	Stream     *streams.Stream
	Cancel     context.CancelFunc
}

type sessionView struct {
	SessionID  string     `json:"session_id"`
	Source     string     `json:"source"`
	Filename   string     `json:"filename"`
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
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

var log = app.GetLogger("recording")

// Init registers the Luxe recording-session API. Recording data is streamed
// directly to the authenticated browser; this package never writes video files
// to the add-on filesystem or Home Assistant /media.
func Init() {
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
	case strings.HasSuffix(route, "/stream") && r.Method == http.MethodGet:
		m.stream(w, r, user, strings.TrimSuffix(route, "/stream"))
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

func (m *manager) authenticate(r *http.Request) (*haUser, int, error) {
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

func (m *manager) stream(w http.ResponseWriter, r *http.Request, user *haUser, id string) {
	s, err := m.getOwned(id, user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	m.mu.Lock()
	if s.State != statePending {
		m.mu.Unlock()
		cancel()
		writeError(w, http.StatusConflict, errNotPending.Error())
		return
	}
	s.State = stateStarting
	s.StartedAt = m.now()
	s.Cancel = cancel
	stream := s.Stream
	m.mu.Unlock()

	medias := mp4.ParseQuery(url.Values{"mp4": []string{"all"}})
	consumer := mp4.NewConsumer(medias)
	consumer.FormatName = "mp4"
	consumer.Protocol = "http"
	consumer.Source = s.Source
	consumer.WithRequest(r)

	if err = stream.AddConsumer(consumer); err != nil {
		cancel()
		m.finish(s, err, nil)
		log.Warn().Err(err).Str("source", s.Source).Msg("recording stream could not start")
		writeError(w, http.StatusBadGateway, "go2rtc could not attach to the camera stream: "+err.Error())
		return
	}

	if ctx.Err() != nil {
		stream.RemoveConsumer(consumer)
		m.finish(s, nil, ctx.Err())
		return
	}

	m.mu.Lock()
	if s.State == stateStarting {
		s.State = stateRecording
	}
	m.mu.Unlock()

	timer := time.AfterFunc(maxDuration, func() {
		m.requestStop(s.ID, "time_limit")
	})
	defer timer.Stop()

	cleanupDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		stream.RemoveConsumer(consumer)
		close(cleanupDone)
	}()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": s.Filename}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)

	_, writeErr := consumer.WriteTo(flushWriter{ResponseWriter: w})
	ctxErr := ctx.Err()
	cancel()
	<-cleanupDone

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

func (m *manager) finish(s *session, writeErr, ctxErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if writeErr != nil && ctxErr == nil {
		s.State = stateFailed
		s.Error = writeErr.Error()
	} else {
		s.State = stateStopped
	}
	if s.FinishedAt.IsZero() {
		s.FinishedAt = m.now()
	}
	s.Cancel = nil
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
}

func (w flushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.Flush()
	return n, err
}

func (w flushWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
