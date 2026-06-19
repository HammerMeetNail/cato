package http

import (
	"encoding/json"
	"net/http"
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
