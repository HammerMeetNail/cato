package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errResp(code, message string) apiError {
	return apiError{Error: code, Message: message}
}

// writeCacheableJSON sends a 200 JSON body for shared catalog data (not
// personal data) with a weak ETag of its content and a short public
// Cache-Control, answering a matching If-None-Match with 304. The ETag is
// weak because gzipMiddleware may change the encoding of the same content.
func writeCacheableJSON(w http.ResponseWriter, r *http.Request, v interface{}, maxAge time.Duration) {
	body, err := json.Marshal(v)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp("internal_error", "Failed to encode response"))
		return
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(maxAge.Seconds())))
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if c := strings.TrimSpace(candidate); c == etag || c == "*" || strings.TrimPrefix(c, "W/") == strings.TrimPrefix(etag, "W/") {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
