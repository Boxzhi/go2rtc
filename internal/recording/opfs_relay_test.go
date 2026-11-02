package recording

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOPFSRelayStreamsChunksToNativeAttachment(t *testing.T) {
	m := newManager("http://127.0.0.1:1/api/auth/current")
	payload := make([]byte, opfsRelayChunkSize+137)
	for i := range payload {
		payload[i] = byte((i*17 + 23) & 0xff)
	}
	wantCRC := crc32.ChecksumIEEE(payload)
	s, err := m.newOPFSRelay("user-1", "luxe-opfs-poc-a-12345678-1234-1234-1234-123456789abc.bin", int64(len(payload)), wantCRC)
	if err != nil {
		t.Fatal(err)
	}
	defer m.expireOPFSRelay(s.id)

	downloadRequest := ingressRequest(http.MethodGet, "/api/luxe/recording-poc/relay/"+s.id+"/download", nil)
	downloadResponse := httptest.NewRecorder()
	downloadDone := make(chan struct{})
	go func() {
		defer close(downloadDone)
		m.downloadOPFSRelay(downloadResponse, downloadRequest, s)
	}()

	offset := 0
	for offset < len(payload) {
		size := opfsRelayChunkSize
		if remaining := len(payload) - offset; remaining < size {
			size = remaining
		}
		body := payload[offset : offset+size]
		request := ingressRequest(http.MethodPost,
			"/api/luxe/recording-poc/relay/"+s.id+"/chunk?offset="+strconv.Itoa(offset),
			bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		response := httptest.NewRecorder()
		m.uploadOPFSRelayChunk(response, request, s)
		if response.Code != http.StatusOK {
			t.Fatalf("chunk at %d failed: status=%d body=%s", offset, response.Code, response.Body.String())
		}
		offset += size
	}

	completeResponse := httptest.NewRecorder()
	m.completeOPFSRelayUpload(completeResponse, s)
	if completeResponse.Code != http.StatusOK {
		t.Fatalf("complete failed: status=%d body=%s", completeResponse.Code, completeResponse.Body.String())
	}

	select {
	case <-downloadDone:
	case <-time.After(3 * time.Second):
		t.Fatal("native attachment response did not finish")
	}

	got := downloadResponse.Body.Bytes()
	if !bytes.Equal(got, payload) {
		t.Fatalf("download payload differs: got %d bytes, want %d", len(got), len(payload))
	}
	if gotCRC := crc32.ChecksumIEEE(got); gotCRC != wantCRC {
		t.Fatalf("download CRC32 = %08x, want %08x", gotCRC, wantCRC)
	}
	if got := downloadResponse.Header().Get("Content-Disposition"); !strings.Contains(got, s.filename) {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := downloadResponse.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("Content-Length = %q", got)
	}
	s.mu.Lock()
	downloadComplete := s.downloadComplete
	downloaded := s.downloadedBytes
	uploadComplete := s.uploadComplete
	s.mu.Unlock()
	if !downloadComplete || !uploadComplete || downloaded != int64(len(payload)) {
		t.Fatalf("relay final state: download=%v upload=%v downloaded=%d", downloadComplete, uploadComplete, downloaded)
	}
}

func TestOPFSRelayRejectsWrongOffsetAndCRC(t *testing.T) {
	t.Run("wrong offset", func(t *testing.T) {
		m := newManager("http://127.0.0.1:1/api/auth/current")
		payload := []byte("small payload")
		s, err := m.newOPFSRelay("user-1", "luxe-opfs-poc-b-12345678-1234-1234-1234-123456789abc.bin", int64(len(payload)), crc32.ChecksumIEEE(payload))
		if err != nil { t.Fatal(err) }
		defer m.expireOPFSRelay(s.id)

		request := ingressRequest(http.MethodPost, "/api/luxe/recording-poc/relay/"+s.id+"/chunk?offset=1", bytes.NewReader(payload))
		request.ContentLength = int64(len(payload))
		response := httptest.NewRecorder()
		m.uploadOPFSRelayChunk(response, request, s)
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
		}
	})

	t.Run("CRC mismatch", func(t *testing.T) {
		m := newManager("http://127.0.0.1:1/api/auth/current")
		payload := []byte("small payload")
		s, err := m.newOPFSRelay("user-1", "luxe-opfs-poc-c-12345678-1234-1234-1234-123456789abc.bin", int64(len(payload)), crc32.ChecksumIEEE([]byte("different")))
		if err != nil { t.Fatal(err) }
		defer m.expireOPFSRelay(s.id)

		downloadRequest := ingressRequest(http.MethodGet, "/api/luxe/recording-poc/relay/"+s.id+"/download", nil)
		downloadResponse := httptest.NewRecorder()
		downloadDone := make(chan struct{})
		go func() {
			defer close(downloadDone)
			m.downloadOPFSRelay(downloadResponse, downloadRequest, s)
		}()

		request := ingressRequest(http.MethodPost, "/api/luxe/recording-poc/relay/"+s.id+"/chunk?offset=0", bytes.NewReader(payload))
		request.ContentLength = int64(len(payload))
		response := httptest.NewRecorder()
		m.uploadOPFSRelayChunk(response, request, s)
		if response.Code != http.StatusOK { t.Fatalf("chunk status=%d", response.Code) }

		completeResponse := httptest.NewRecorder()
		m.completeOPFSRelayUpload(completeResponse, s)
		if completeResponse.Code != http.StatusUnprocessableEntity {
			t.Fatalf("complete status=%d body=%s", completeResponse.Code, completeResponse.Body.String())
		}
		select {
		case <-downloadDone:
		case <-time.After(3*time.Second):
			t.Fatal("download did not stop after CRC failure")
		}
	})
}

func TestOPFSRelayCreateEndpointReturnsIngressScopedURLs(t *testing.T) {
	m := newManager("http://127.0.0.1:1/api/auth/current")
	payload := []byte("test")
	body, _ := json.Marshal(opfsRelayCreateRequest{
		Filename: "luxe-opfs-poc-a-12345678-1234-1234-1234-123456789abc.bin",
		SizeBytes: int64(len(payload)),
		CRC32: fmt.Sprintf("%08x", crc32.ChecksumIEEE(payload)),
	})
	request := ingressRequest(http.MethodPost, "/rtc/api/luxe/recording-poc/relay", bytes.NewReader(body))
	response := httptest.NewRecorder()
	m.handleOPFSRelay(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		UploadURL string `json:"upload_url"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
	wantPrefix := "/api/hassio_ingress/test-session-token/rtc/api/luxe/recording-poc/relay/"
	if !strings.HasPrefix(result.UploadURL, wantPrefix) || !strings.HasSuffix(result.UploadURL, "/chunk") {
		t.Fatalf("upload URL = %q", result.UploadURL)
	}
	if !strings.HasPrefix(result.DownloadURL, wantPrefix) || !strings.HasSuffix(result.DownloadURL, "/download") {
		t.Fatalf("download URL = %q", result.DownloadURL)
	}
}

func TestOPFSRelayRejectsUntrustedRequests(t *testing.T) {
	m := newManager("http://127.0.0.1:1/api/auth/current")
	request := httptest.NewRequest(http.MethodPost, "/api/luxe/recording-poc/relay", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	m.handleOPFSRelay(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", response.Code)
	}
}

func ingressRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.RemoteAddr = "172.30.32.2:54210"
	request.Header.Set("X-Ingress-Path", "/api/hassio_ingress/test-session-token")
	request.Header.Set("X-Remote-User-Id", "user-1")
	return request
}
