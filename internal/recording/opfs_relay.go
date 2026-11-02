package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/google/uuid"
)

const (
	opfsRelayAPIPath = "/api/luxe/recording-poc/relay"
	opfsRelayChunkSize = 256 * 1024
	opfsRelayMaxSize int64 = 64 << 20
	opfsRelayMaxSessions = 3
	opfsRelayTTL = 10 * time.Minute
)

type opfsRelaySession struct {
	id string
	userID string
	filename string
	sizeBytes int64
	expectedCRC uint32
	createdAt time.Time

	reader *io.PipeReader
	writer *io.PipeWriter
	hash hash.Hash32

	mu sync.Mutex
	uploadMu sync.Mutex
	nextOffset int64
	uploadedBytes int64
	downloadedBytes int64
	uploadComplete bool
	downloadStarted bool
	downloadComplete bool
	err string
	expiry *time.Timer
}

type opfsRelayCreateRequest struct {
	Filename string `json:"filename"`
	SizeBytes int64 `json:"size_bytes"`
	CRC32 string `json:"crc32"`
}

func (m *manager) handleOPFSRelay(w http.ResponseWriter, r *http.Request) {
	user, status, err := m.authenticate(r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	index := strings.LastIndex(r.URL.Path, opfsRelayAPIPath)
	if index < 0 {
		writeError(w, http.StatusNotFound, "relay endpoint not found")
		return
	}
	tail := strings.Trim(strings.TrimPrefix(r.URL.Path[index+len(opfsRelayAPIPath):], "/"), "/")
	if tail == "" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.createOPFSRelay(w, r, user)
		return
	}

	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "relay endpoint not found")
		return
	}
	s, err := m.getOPFSRelay(parts[0], user.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	switch parts[1] {
	case "chunk":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.uploadOPFSRelayChunk(w, r, s)
	case "complete":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.completeOPFSRelayUpload(w, s)
	case "abort":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.abortOPFSRelay(w, s, errors.New("client aborted OPFS relay"))
	case "download":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.downloadOPFSRelay(w, r, s)
	case "status":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		m.statusOPFSRelay(w, s)
	default:
		writeError(w, http.StatusNotFound, "relay endpoint not found")
	}
}

func (m *manager) createOPFSRelay(w http.ResponseWriter, r *http.Request, user *haUser) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()

	var input opfsRelayCreateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid OPFS relay request")
		return
	}
	if !validOPFSRelayFilename(input.Filename) {
		writeError(w, http.StatusBadRequest, "invalid OPFS test filename")
		return
	}
	if input.SizeBytes <= 0 || input.SizeBytes > opfsRelayMaxSize {
		writeError(w, http.StatusBadRequest, "file size must be between 1 byte and 64 MiB")
		return
	}
	crcValue, err := strconv.ParseUint(strings.TrimSpace(input.CRC32), 16, 32)
	if err != nil || len(input.CRC32) != 8 {
		writeError(w, http.StatusBadRequest, "CRC32 must contain exactly 8 hexadecimal characters")
		return
	}

	s, err := m.newOPFSRelay(user.ID, input.Filename, input.SizeBytes, uint32(crcValue))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	apiURL := m.opfsRelayURL(r, s.id, "chunk")
	api.ResponseJSON(w, map[string]any{
		"ok": true,
		"session_id": s.id,
		"filename": s.filename,
		"size_bytes": s.sizeBytes,
		"crc32": fmt.Sprintf("%08x", s.expectedCRC),
		"chunk_size": opfsRelayChunkSize,
		"upload_url": apiURL,
		"complete_url": m.opfsRelayURL(r, s.id, "complete"),
		"abort_url": m.opfsRelayURL(r, s.id, "abort"),
		"status_url": m.opfsRelayURL(r, s.id, "status"),
		"download_url": m.opfsRelayURL(r, s.id, "download"),
	})
}

func (m *manager) newOPFSRelay(userID, filename string, sizeBytes int64, expectedCRC uint32) (*opfsRelaySession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	active := 0
	for _, current := range m.opfsRelays {
		current.mu.Lock()
		if current.err == "" && !current.downloadComplete {
			active++
		}
		current.mu.Unlock()
	}
	if active >= opfsRelayMaxSessions {
		return nil, errors.New("maximum number of simultaneous OPFS relay downloads reached")
	}

	reader, writer := io.Pipe()
	s := &opfsRelaySession{
		id: uuid.NewString(),
		userID: userID,
		filename: filename,
		sizeBytes: sizeBytes,
		expectedCRC: expectedCRC,
		createdAt: now,
		reader: reader,
		writer: writer,
		hash: crc32.NewIEEE(),
	}
	if m.opfsRelays == nil {
		m.opfsRelays = make(map[string]*opfsRelaySession)
	}
	m.opfsRelays[s.id] = s
	s.expiry = time.AfterFunc(opfsRelayTTL, func() {
		m.expireOPFSRelay(s.id)
	})
	return s, nil
}

func validOPFSRelayFilename(filename string) bool {
	if len(filename) < len("luxe-opfs-poc-a-00000000.bin") || len(filename) > 128 {
		return false
	}
	if filename != path.Base(filename) || !strings.HasPrefix(filename, "luxe-opfs-poc-") || !strings.HasSuffix(filename, ".bin") {
		return false
	}
	for _, r := range filename {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._-", r) {
			return false
		}
	}
	return true
}

func (m *manager) opfsRelayURL(r *http.Request, id, action string) string {
	index := strings.LastIndex(r.URL.Path, opfsRelayAPIPath)
	basePath := ""
	if index >= 0 {
		basePath = strings.TrimRight(r.URL.Path[:index], "/")
	}
	ingressPath := strings.TrimRight(r.Header.Get("X-Ingress-Path"), "/")
	if !strings.HasPrefix(ingressPath, "/api/hassio_ingress/") || strings.Contains(ingressPath, "..") {
		ingressPath = ""
	}
	return ingressPath + basePath + opfsRelayAPIPath + "/" + id + "/" + action
}

func (m *manager) getOPFSRelay(id, userID string) (*opfsRelaySession, error) {
	m.mu.Lock()
	s := m.opfsRelays[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("OPFS relay session not found")
	}
	s.mu.Lock()
	owned := s.userID == userID
	expired := time.Since(s.createdAt) > opfsRelayTTL
	s.mu.Unlock()
	if !owned || expired {
		return nil, errors.New("OPFS relay session not found")
	}
	return s, nil
}

func (m *manager) uploadOPFSRelayChunk(w http.ResponseWriter, r *http.Request, s *opfsRelaySession) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "a valid chunk offset is required")
		return
	}
	if r.ContentLength <= 0 || r.ContentLength > opfsRelayChunkSize {
		writeError(w, http.StatusLengthRequired, "chunk Content-Length must be between 1 and 256 KiB")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, opfsRelayChunkSize)
	defer r.Body.Close()

	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()

	s.mu.Lock()
	if s.err != "" {
		message := s.err
		s.mu.Unlock()
		writeError(w, http.StatusGone, message)
		return
	}
	if s.uploadComplete {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "OPFS relay upload is already complete")
		return
	}
	if offset != s.nextOffset {
		expected := s.nextOffset
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Sprintf("unexpected chunk offset: got %d, want %d", offset, expected))
		return
	}
	remaining := s.sizeBytes - s.nextOffset
	chunkBytes := int64(opfsRelayChunkSize)
	if remaining < chunkBytes {
		chunkBytes = remaining
	}
	if r.ContentLength != chunkBytes {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, fmt.Sprintf("chunk size must be %d bytes", chunkBytes))
		return
	}
	s.mu.Unlock()

	written, err := io.CopyN(io.MultiWriter(s.writer, s.hash), r.Body, chunkBytes)
	if err != nil || written != chunkBytes {
		if err == nil {
			err = io.ErrShortWrite
		}
		s.fail(err)
		writeError(w, http.StatusBadGateway, "OPFS relay download consumer disconnected: "+err.Error())
		return
	}

	s.mu.Lock()
	s.nextOffset += written
	s.uploadedBytes = s.nextOffset
	nextOffset := s.nextOffset
	s.mu.Unlock()

	api.ResponseJSON(w, map[string]any{
		"ok": true,
		"written_bytes": written,
		"next_offset": nextOffset,
	})
}

func (m *manager) completeOPFSRelayUpload(w http.ResponseWriter, s *opfsRelaySession) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()

	s.mu.Lock()
	if s.err != "" {
		message := s.err
		s.mu.Unlock()
		writeError(w, http.StatusGone, message)
		return
	}
	if s.uploadComplete {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "OPFS relay upload is already complete")
		return
	}
	if s.nextOffset != s.sizeBytes {
		got, want := s.nextOffset, s.sizeBytes
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Sprintf("upload is incomplete: received %d of %d bytes", got, want))
		return
	}
	gotCRC := s.hash.Sum32()
	if gotCRC != s.expectedCRC {
		s.mu.Unlock()
		err := fmt.Errorf("OPFS relay CRC32 mismatch: got %08x, want %08x", gotCRC, s.expectedCRC)
		s.fail(err)
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.uploadComplete = true
	s.mu.Unlock()

	if err := s.writer.Close(); err != nil {
		s.fail(err)
		writeError(w, http.StatusBadGateway, "could not close OPFS relay upload: "+err.Error())
		return
	}
	api.ResponseJSON(w, map[string]any{
		"ok": true,
		"upload_complete": true,
		"size_bytes": s.sizeBytes,
		"crc32": fmt.Sprintf("%08x", gotCRC),
	})
}

func (m *manager) downloadOPFSRelay(w http.ResponseWriter, r *http.Request, s *opfsRelaySession) {
	s.mu.Lock()
	if s.err != "" {
		message := s.err
		s.mu.Unlock()
		writeError(w, http.StatusGone, message)
		return
	}
	if s.downloadStarted {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "OPFS relay download has already been claimed")
		return
	}
	s.downloadStarted = true
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(s.sizeBytes, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": s.filename}))
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, no-transform")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Luxe-Relay-Size", strconv.FormatInt(s.sizeBytes, 10))
	w.Header().Set("X-Luxe-Relay-CRC32", fmt.Sprintf("%08x", s.expectedCRC))
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	writer := &opfsRelayResponseWriter{ResponseWriter: w, session: s}
	written, err := io.Copy(writer, s.reader)
	if err == nil && written != s.sizeBytes {
		err = fmt.Errorf("relay ended early: sent %d of %d bytes", written, s.sizeBytes)
	}
	if r.Context().Err() != nil && err == nil {
		err = r.Context().Err()
	}
	s.mu.Lock()
	if err != nil {
		s.err = err.Error()
	} else {
		s.downloadComplete = true
	}
	s.mu.Unlock()
	_ = s.reader.Close()
	if err != nil {
		log.Warn().Err(err).Str("relay", s.id).Msg("OPFS relay download ended with an error")
	}
}

type opfsRelayResponseWriter struct {
	http.ResponseWriter
	session *opfsRelaySession
}

func (w *opfsRelayResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.session.mu.Lock()
		w.session.downloadedBytes += int64(n)
		w.session.mu.Unlock()
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return n, err
}

func (m *manager) statusOPFSRelay(w http.ResponseWriter, s *opfsRelaySession) {
	s.mu.Lock()
	view := map[string]any{
		"session_id": s.id,
		"filename": s.filename,
		"size_bytes": s.sizeBytes,
		"crc32": fmt.Sprintf("%08x", s.expectedCRC),
		"uploaded_bytes": s.uploadedBytes,
		"downloaded_bytes": s.downloadedBytes,
		"upload_complete": s.uploadComplete,
		"download_started": s.downloadStarted,
		"download_complete": s.downloadComplete,
	}
	if s.err != "" {
		view["error"] = s.err
	}
	s.mu.Unlock()
	api.ResponseJSON(w, view)
}

func (m *manager) abortOPFSRelay(w http.ResponseWriter, s *opfsRelaySession, reason error) {
	s.fail(reason)
	api.ResponseJSON(w, map[string]any{"ok": true, "aborted": true})
}

func (m *manager) expireOPFSRelay(id string) {
	m.mu.Lock()
	s := m.opfsRelays[id]
	delete(m.opfsRelays, id)
	m.mu.Unlock()
	if s != nil {
		s.fail(errors.New("OPFS relay session expired"))
	}
}

func (s *opfsRelaySession) fail(err error) {
	if err == nil {
		err = errors.New("OPFS relay failed")
	}
	s.mu.Lock()
	if s.err == "" {
		s.err = err.Error()
	}
	s.mu.Unlock()
	_ = s.writer.CloseWithError(err)
	_ = s.reader.CloseWithError(err)
}
