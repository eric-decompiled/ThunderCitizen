// Package httperr provides consistent JSON error responses for HTTP APIs.
package httperr

import (
	"encoding/json"
	"net/http"

	"thundercitizen/internal/logger"
)

var log = logger.New("http")

// Response is the standard JSON error body.
type Response struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// Write sends a JSON error response with the given status code and message.
func Write(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(Response{Error: msg, Code: code})
}

// BadRequest sends a 400 error. No server-side log — 400s are almost
// always the client's fault and logging every parse error turns logs
// into noise. If you DO have a server-side reason (malformed stored
// config that made a parse fail), use BadRequestErr.
func BadRequest(w http.ResponseWriter, msg string) {
	Write(w, http.StatusBadRequest, msg)
}

// BadRequestErr logs the underlying error and sends a 400 with the given
// client-facing message. Use sparingly — most 400s don't warrant a log
// line.
func BadRequestErr(w http.ResponseWriter, msg string, err error) {
	log.Error("bad request", "msg", msg, "err", err)
	Write(w, http.StatusBadRequest, msg)
}

// NotFound sends a 404 error. No server-side log — a missing row for a
// valid URL is an expected failure mode (user typed a bogus ID, bot
// scanning for PHP files, etc.). For "lookup that might be either 'no
// rows' or 'query actually failed'", distinguish the two at the call
// site with errors.Is(err, pgx.ErrNoRows) and route real errors to
// Internal() so they get logged.
func NotFound(w http.ResponseWriter, msg string) {
	Write(w, http.StatusNotFound, msg)
}

// NotFoundErr logs the underlying error and sends a 404. Use when you
// genuinely want operators to see every 404 in logs (rare).
func NotFoundErr(w http.ResponseWriter, msg string, err error) {
	log.Error("not found", "msg", msg, "err", err)
	Write(w, http.StatusNotFound, msg)
}

// Internal logs the error and sends a generic 500 to the client.
func Internal(w http.ResponseWriter, err error) {
	log.Error("internal error", "err", err)
	Write(w, http.StatusInternalServerError, "internal server error")
}

// Unavailable sends a 503 error. Use when there's no underlying error to
// log (e.g., a cache is warming, an upstream feed is not yet populated).
// If you DO have an underlying error you want in the logs, use
// UnavailableErr instead.
func Unavailable(w http.ResponseWriter, msg string) {
	Write(w, http.StatusServiceUnavailable, msg)
}

// UnavailableErr logs the underlying error and sends a 503 with the given
// client-facing message. Mirrors the Internal helper's log-then-write
// pattern — operators see the cause in logs, the client sees a generic
// message. Use for /health DB ping failures, dependency timeouts, etc.
func UnavailableErr(w http.ResponseWriter, msg string, err error) {
	log.Error("service unavailable", "msg", msg, "err", err)
	Write(w, http.StatusServiceUnavailable, msg)
}
