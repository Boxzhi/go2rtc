package recording

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"luxe-xiaomi-客厅-移动镜头-20261101-120000.mp4", "luxe-xiaomi-客厅-移动镜头-20261101-120000.mp4"},
		{"../../bad/name.mp4", "name.mp4"},
		{"bad\\name", "name.mp4"},
		{"", "luxe-camera.mp4"},
		{"a?b:c.mp4", "a_b_c.mp4"},
	}

	for _, test := range tests {
		if got := sanitizeFilename(test.input); got != test.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestCreateEnforcesThreeSessionsAndOnePerSource(t *testing.T) {
	m := newManager("http://127.0.0.1")
	stream := &streams.Stream{}

	if _, err := m.create("camera-1", "one.mp4", "user-1", stream); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("camera-1", "duplicate.mp4", "user-1", stream); !errors.Is(err, errDuplicate) {
		t.Fatalf("expected duplicate-source error, got %v", err)
	}
	if _, err := m.create("camera-2", "two.mp4", "user-1", stream); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("camera-3", "three.mp4", "user-1", stream); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("camera-4", "four.mp4", "user-1", stream); !errors.Is(err, errMaxSessions) {
		t.Fatalf("expected max-session error, got %v", err)
	}
}

func TestPendingSessionCanBeStoppedIdempotently(t *testing.T) {
	m := newManager("http://127.0.0.1")
	s, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}

	m.requestStop(s.ID, "user")
	m.requestStop(s.ID, "user")

	m.mu.Lock()
	defer m.mu.Unlock()
	if s.State != stateStopped {
		t.Fatalf("state = %q, want %q", s.State, stateStopped)
	}
	if s.FinishedAt.IsZero() {
		t.Fatal("finished_at was not set")
	}
}

func TestAuthenticateUsesHomeAssistantCurrentUserEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/current" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer valid-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-123","is_admin":false}`))
	}))
	defer server.Close()

	m := newManager(server.URL + "/api/auth/current")
	m.client = server.Client()
	req := httptest.NewRequest(http.MethodGet, "/api/luxe/recordings", nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	user, status, err := m.authenticate(req)
	if err != nil {
		t.Fatalf("authenticate returned error: %v", err)
	}
	if status != 0 || user.ID != "user-123" {
		t.Fatalf("user=%+v status=%d", user, status)
	}
}

func TestAuthenticateRejectsInvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	m := newManager(server.URL)
	m.client = server.Client()
	req := httptest.NewRequest(http.MethodGet, "/api/luxe/recordings", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")

	_, status, err := m.authenticate(req)
	if err == nil || status != http.StatusUnauthorized {
		t.Fatalf("expected 401, got status=%d err=%v", status, err)
	}
}

func TestDownloadURLUsesIngressPathAndGo2RTCBasePath(t *testing.T) {
	m := newManager("http://127.0.0.1")
	req := httptest.NewRequest(http.MethodPost, "/rtc/api/luxe/recordings/start", nil)
	req.Header.Set("X-Ingress-Path", "/api/hassio_ingress/secret-token/")

	got := m.downloadURL(req, "session-id")
	want := "/api/hassio_ingress/secret-token/rtc/api/luxe/recordings/session-id/stream"
	if got != want {
		t.Fatalf("downloadURL = %q, want %q", got, want)
	}
}

func TestPendingSessionExpires(t *testing.T) {
	m := newManager("http://127.0.0.1")
	now := time.Now()
	m.now = func() time.Time { return now }

	s, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	m.pruneLocked(now.Add(pendingTTL + time.Second))
	m.mu.Unlock()

	if s.State != stateExpired {
		t.Fatalf("state = %q, want %q", s.State, stateExpired)
	}
}


func TestStoppingOneSessionLeavesOtherSessionsActive(t *testing.T) {
	m := newManager("http://127.0.0.1")
	stream := &streams.Stream{}

	one, err := m.create("camera-1", "one.mp4", "user-1", stream)
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.create("camera-2", "two.mp4", "user-1", stream)
	if err != nil {
		t.Fatal(err)
	}
	three, err := m.create("camera-3", "three.mp4", "user-1", stream)
	if err != nil {
		t.Fatal(err)
	}

	m.requestStop(one.ID, "user")
	if one.State != stateStopped {
		t.Fatalf("stopped session state = %q, want %q", one.State, stateStopped)
	}
	if two.State != statePending || three.State != statePending {
		t.Fatalf("stopping one session changed other states: %q, %q", two.State, three.State)
	}
	if _, err = m.create("camera-4", "four.mp4", "user-1", stream); err != nil {
		t.Fatalf("a freed slot should accept another session: %v", err)
	}
}

func TestDownloadCookieIsPathScopedAndSingleUse(t *testing.T) {
	m := newManager("http://127.0.0.1")
	s, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/luxe/recordings/start", nil)
	startReq.Header.Set("X-Ingress-Path", "/api/hassio_ingress/test-token/")
	startReq.Header.Set("Origin", "https://ha.example")
	downloadURL := m.downloadURL(startReq, s.ID)

	response := httptest.NewRecorder()
	if err = m.setDownloadCookie(response, startReq, s, downloadURL); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Path != downloadURL {
		t.Fatalf("cookie path = %q, want %q", cookie.Path, downloadURL)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("download cookie security attributes are incomplete: %+v", cookie)
	}

	streamReq := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	streamReq.AddCookie(cookie)
	_, cancel := context.WithCancel(streamReq.Context())
	defer cancel()

	claimed, status, err := m.claimDownload(streamReq, s.ID, cancel)
	if err != nil || status != 0 || claimed != s {
		t.Fatalf("first stream claim failed: session=%v status=%d err=%v", claimed, status, err)
	}

	_, secondCancel := context.WithCancel(streamReq.Context())
	defer secondCancel()
	if _, status, err = m.claimDownload(streamReq, s.ID, secondCancel); err == nil || status != http.StatusConflict {
		t.Fatalf("second stream claim should be rejected, status=%d err=%v", status, err)
	}
}

func TestInvalidDownloadCookieIsRejected(t *testing.T) {
	m := newManager("http://127.0.0.1")
	s, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/luxe/recordings/"+s.ID+"/stream", nil)
	req.AddCookie(&http.Cookie{Name: downloadCookieName(s.ID), Value: "not-the-token"})
	_, cancel := context.WithCancel(req.Context())
	defer cancel()

	if _, status, err := m.claimDownload(req, s.ID, cancel); err == nil || status != http.StatusUnauthorized {
		t.Fatalf("invalid cookie should be rejected, status=%d err=%v", status, err)
	}
}

func TestClientDisconnectMarksSessionInterrupted(t *testing.T) {
	m := newManager("http://127.0.0.1")
	s, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}
	s.State = stateRecording

	m.finish(s, nil, context.Canceled)
	if s.State != stateInterrupted {
		t.Fatalf("state = %q, want %q", s.State, stateInterrupted)
	}
	if s.StopReason != "client_disconnect" {
		t.Fatalf("stop reason = %q, want client_disconnect", s.StopReason)
	}
}


func TestTransportDropStopsOnlyAffectedSession(t *testing.T) {
	m := newManager("http://127.0.0.1")
	one, err := m.create("camera-1", "one.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.create("camera-2", "two.mp4", "user-1", &streams.Stream{})
	if err != nil {
		t.Fatal(err)
	}

	ctxOne, cancelOne := context.WithCancel(context.Background())
	defer cancelOne()
	ctxTwo, cancelTwo := context.WithCancel(context.Background())
	defer cancelTwo()

	one.State = stateRecording
	one.Cancel = cancelOne
	two.State = stateRecording
	two.Cancel = cancelTwo

	m.requestFailure(one.ID, "transport_drops", "packet loss")

	if one.State != stateStopping || ctxOne.Err() != context.Canceled {
		t.Fatalf("affected session was not stopped: state=%q context=%v", one.State, ctxOne.Err())
	}
	if two.State != stateRecording || ctxTwo.Err() != nil {
		t.Fatalf("unrelated session was affected: state=%q context=%v", two.State, ctxTwo.Err())
	}

	m.finish(one, nil, ctxOne.Err())
	if one.State != stateFailed || one.Error != "packet loss" {
		t.Fatalf("packet-loss session should fail: state=%q error=%q", one.State, one.Error)
	}
}


func TestThreeRecordingDownloadsCanBeClaimedConcurrently(t *testing.T) {
	m := newManager("http://127.0.0.1")
	type claimInput struct {
		id  string
		req *http.Request
	}
	inputs := make([]claimInput, 0, maxSessions)

	for i := 0; i < maxSessions; i++ {
		source := fmt.Sprintf("camera-%d", i+1)
		s, err := m.create(source, source+".mp4", "user-1", &streams.Stream{})
		if err != nil {
			t.Fatal(err)
		}

		startReq := httptest.NewRequest(http.MethodPost, "/api/luxe/recordings/start", nil)
		startReq.Header.Set("X-Ingress-Path", "/api/hassio_ingress/test-token/")
		downloadURL := m.downloadURL(startReq, s.ID)
		response := httptest.NewRecorder()
		if err = m.setDownloadCookie(response, startReq, s, downloadURL); err != nil {
			t.Fatal(err)
		}

		streamReq := httptest.NewRequest(http.MethodGet, downloadURL, nil)
		streamReq.AddCookie(response.Result().Cookies()[0])
		inputs = append(inputs, claimInput{id: s.ID, req: streamReq})
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(inputs))
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cancel := context.WithCancel(input.req.Context())
			defer cancel()

			s, status, err := m.claimDownload(input.req, input.id, cancel)
			if err != nil {
				errs <- fmt.Errorf("claim %s failed with status %d: %w", input.id, status, err)
				return
			}
			if s.State != stateStarting {
				errs <- fmt.Errorf("claim %s state = %q, want %q", input.id, s.State, stateStarting)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
