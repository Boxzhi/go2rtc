package recording

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const downloadCookiePrefix = "luxe_recording_"

func downloadCookieName(id string) string {
	return downloadCookiePrefix + strings.ReplaceAll(id, "-", "")
}

// setDownloadCookie issues a one-session capability cookie. The cookie is
// HttpOnly and scoped to the exact download URL so a native browser navigation
// can start the MP4 download without placing a Home Assistant token in the URL
// or reading the media response through JavaScript.
func (m *manager) setDownloadCookie(w http.ResponseWriter, r *http.Request, s *session, downloadURL string) error {
	parsed, err := url.Parse(downloadURL)
	if err != nil || !strings.HasPrefix(parsed.Path, "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid recording download URL")
	}

	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	tokenHash := sha256.Sum256([]byte(token))
	secure := isSecureRequest(r)

	m.mu.Lock()
	if m.sessions[s.ID] != s {
		m.mu.Unlock()
		return errors.New("recording session no longer exists")
	}
	s.DownloadTokenHash = tokenHash
	s.CookiePath = parsed.Path
	s.CookieSecure = secure
	m.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     downloadCookieName(s.ID),
		Value:    token,
		Path:     parsed.Path,
		Expires:  m.now().Add(maxDuration + pendingTTL),
		MaxAge:   int((maxDuration + pendingTTL).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && strings.EqualFold(origin.Scheme, "https")
}

// claimDownload validates the path-scoped cookie and atomically claims the
// pending session. A session can have only one MP4 response, while other
// sessions remain fully independent and may stream concurrently.
func (m *manager) claimDownload(r *http.Request, id string, cancel context.CancelFunc) (*session, int, error) {
	cookie, err := r.Cookie(downloadCookieName(id))
	if err != nil {
		return nil, http.StatusUnauthorized, errors.New("recording download cookie is missing")
	}
	tokenHash := sha256.Sum256([]byte(cookie.Value))

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now())

	s := m.sessions[id]
	if s == nil {
		return nil, http.StatusNotFound, errors.New("recording session not found")
	}
	if subtle.ConstantTimeCompare(s.DownloadTokenHash[:], tokenHash[:]) != 1 {
		return nil, http.StatusUnauthorized, errors.New("recording download cookie is invalid")
	}
	if s.StreamClaimed || s.State != statePending {
		return nil, http.StatusConflict, errNotPending
	}

	s.StreamClaimed = true
	s.State = stateStarting
	s.StartedAt = m.now()
	s.Cancel = cancel
	return s, 0, nil
}

func (m *manager) clearDownloadCookie(w http.ResponseWriter, s *session) {
	if s.CookiePath == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     downloadCookieName(s.ID),
		Value:    "",
		Path:     s.CookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (m *manager) discard(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

var _ = time.Second
