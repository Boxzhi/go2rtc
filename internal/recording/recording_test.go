package recording

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
