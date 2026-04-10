package api

import (
	"encoding/json"
	"net/http"
)

// API error codes. String values are stable and sent to the client — they
// must not be changed without updating the OpenAPI specification.
const (
	ErrorCodeBadRequest       = "bad_request"
	ErrorCodeUnauthorized     = "unauthorized"
	ErrorCodeForbidden        = "forbidden"
	ErrorCodeNotFound         = "not_found"
	ErrorCodeMethodNotAllowed = "method_not_allowed"
	ErrorCodeConflict         = "conflict"
	ErrorCodeUnprocessable    = "unprocessable_entity"
	ErrorCodeRateLimited      = "rate_limited"
	ErrorCodeInternal         = "internal"
	ErrorCodeUnavailable      = "unavailable"
	ErrorCodeTimeout          = "timeout"
)

// ErrorResponse wraps the error body. The format `{"error": {"code": "...", "message": "..."}}`
// is fixed in the OpenAPI specification.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody is the payload of a single error.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError serialises an error into the response.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

// notFoundHandler is the default 404 handler in JSON format.
func notFoundHandler(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotFound, ErrorCodeNotFound, "resource not found")
}

// methodNotAllowedHandler is the default 405 handler in JSON format.
func methodNotAllowedHandler(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusMethodNotAllowed, ErrorCodeMethodNotAllowed, "method not allowed")
}
