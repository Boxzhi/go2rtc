package recording

import (
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNativeDownloadPOCStreamsAuthenticatedAttachment(t *testing.T) {
	m := newManager("http://127.0.0.1:1/api/auth/current")
	request := httptest.NewRequest(http.MethodGet, "/api/luxe/recording-poc/download", nil)
	request.RemoteAddr = "172.30.32.2:54210"
	request.Header.Set("X-Ingress-Path", "/api/hassio_ingress/test-session-token")
	request.Header.Set("X-Remote-User-Id", "poc-user")

	response := httptest.NewRecorder()
	m.nativeDownloadPOC(response, request)

	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", result.StatusCode, http.StatusOK, response.Body.String())
	}
	if got := result.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := result.Header.Get("Content-Length"); got != strconv.FormatInt(nativeDownloadPOCSize, 10) {
		t.Fatalf("Content-Length = %q", got)
	}
	if got := result.Header.Get("Content-Disposition"); !strings.Contains(got, nativeDownloadPOCFilename) || !strings.HasPrefix(got, "attachment") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := result.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") || !strings.Contains(got, "no-transform") {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := result.Header.Get("X-Luxe-POC-Size"); got != strconv.FormatInt(nativeDownloadPOCSize, 10) {
		t.Fatalf("X-Luxe-POC-Size = %q", got)
	}

	body := response.Body.Bytes()
	if int64(len(body)) != nativeDownloadPOCSize {
		t.Fatalf("body size = %d, want %d", len(body), nativeDownloadPOCSize)
	}
	gotCRC := crc32.ChecksumIEEE(body)
	wantCRC, err := strconv.ParseUint(result.Header.Get("X-Luxe-POC-CRC32"), 16, 32)
	if err != nil {
		t.Fatalf("invalid CRC32 header: %v", err)
	}
	if gotCRC != uint32(wantCRC) {
		t.Fatalf("body CRC32 = %08x, header = %08x", gotCRC, uint32(wantCRC))
	}
}

func TestNativeDownloadPOCRejectsUntrustedRequest(t *testing.T) {
	m := newManager("http://127.0.0.1:1/api/auth/current")
	request := httptest.NewRequest(http.MethodGet, "/api/luxe/recording-poc/download", nil)
	response := httptest.NewRecorder()

	m.nativeDownloadPOC(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
