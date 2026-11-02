package recording

import (
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"net/http"
	"strconv"
)

const (
	nativeDownloadPOCSize       int64 = 16 << 20
	nativeDownloadPOCChunkSize        = 64 << 10
	nativeDownloadPOCFilename         = "luxe-wkdownload-poc-16mib.bin"
)

// nativeDownloadPOC serves a deterministic attachment to verify that an
// embedded WKWebView hands an HTTP attachment navigation to its native
// download manager. The payload is generated in bounded chunks; no recording
// file or full-response buffer is created on the server.
func (m *manager) nativeDownloadPOC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if _, status, err := m.authenticate(r); err != nil {
		writeError(w, status, err.Error())
		return
	}

	checksum := crc32.NewIEEE()
	if err := writeNativeDownloadPOCPayload(checksum, nativeDownloadPOCSize); err != nil {
		writeError(w, http.StatusInternalServerError, "could not prepare test checksum")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(nativeDownloadPOCSize, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": nativeDownloadPOCFilename,
	}))
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, no-transform")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Luxe-POC-Size", strconv.FormatInt(nativeDownloadPOCSize, 10))
	w.Header().Set("X-Luxe-POC-CRC32", fmt.Sprintf("%08x", checksum.Sum32()))
	w.WriteHeader(http.StatusOK)

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if err := writeNativeDownloadPOCPayload(w, nativeDownloadPOCSize); err != nil {
		log.Warn().Err(err).Msg("WKDownload POC response interrupted")
	}
}

// writeNativeDownloadPOCPayload emits a deterministic byte pattern using one
// 64 KiB scratch buffer. It is used once to calculate the checksum and once
// to stream the response; memory usage stays bounded regardless of file size.
func writeNativeDownloadPOCPayload(dst io.Writer, total int64) error {
	buffer := make([]byte, nativeDownloadPOCChunkSize)
	for offset := int64(0); offset < total; {
		length := int64(len(buffer))
		if remaining := total - offset; remaining < length {
			length = remaining
		}
		chunk := buffer[:int(length)]
		for index := range chunk {
			chunk[index] = byte((offset + int64(index))*31 + 7)
		}

		written, err := dst.Write(chunk)
		if err != nil {
			return err
		}
		if written != len(chunk) {
			return io.ErrShortWrite
		}
		offset += int64(written)
	}
	return nil
}
