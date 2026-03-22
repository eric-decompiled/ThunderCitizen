// Package middleware provides HTTP middleware for the application.
package middleware

import (
	"net/http"
	"runtime/debug"
	"time"

	"thundercitizen/internal/logger"
)

var log = logger.New("http")

// RequestLogger logs method, path, status, and duration for every request.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start),
		)
	})
}

// Recoverer catches panics, logs the stack trace, and returns 500.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Error("panic recovered",
					"err", err,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal server error","code":500}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecureHeaders sets basic security headers on every response.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// PageCache sets a default Cache-Control header for HTML page routes.
// Pass one of the strategies from internal/cache (typically cache.Page).
// Inner handlers can still override by setting their own Cache-Control
// before the first Write — e.g. a live page that needs no-cache.
func PageCache(value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", value)
			next.ServeHTTP(w, r)
		})
	}
}

// NoCacheInDev overwrites Cache-Control to "no-store" on every response
// whenever the environment is anything other than "production". Use it as
// the outermost cache middleware so it neutralizes whatever any inner
// handler set — there are 20+ Cache-Control call sites scattered across
// the page handlers, transit API, and static-file shim, and rather than
// audit each one for staleness during development we punt that whole
// problem at the boundary.
//
// In production this is a no-op: NoCacheInDev returns the next handler
// untouched, so production keeps every Cache-Control / max-age value the
// inner handlers chose. Zero perf cost in prod, zero stale work in dev.
//
// Implementation note: we can't just set the header at the top of the
// chain because inner handlers explicitly call w.Header().Set("Cache-
// Control", ...) before WriteHeader, which clobbers our value. Instead we
// wrap ResponseWriter and overwrite the header at the moment headers are
// flushed (the first WriteHeader OR Write call). This catches every
// inner Set call regardless of when it ran.
func NoCacheInDev(env string) func(http.Handler) http.Handler {
	if env == "production" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&noCacheWriter{ResponseWriter: w}, r)
		})
	}
}

// noCacheWriter intercepts WriteHeader and Write to overwrite the
// Cache-Control header (and the legacy Pragma / Expires headers) right
// before the first byte is sent. Used by NoCacheInDev.
type noCacheWriter struct {
	http.ResponseWriter
	stamped bool
}

func (w *noCacheWriter) stampNoCache() {
	if w.stamped {
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")
	w.stamped = true
}

func (w *noCacheWriter) WriteHeader(code int) {
	w.stampNoCache()
	w.ResponseWriter.WriteHeader(code)
}

func (w *noCacheWriter) Write(b []byte) (int, error) {
	w.stampNoCache()
	return w.ResponseWriter.Write(b)
}

// Flush implements http.Flusher for SSE compatibility — the transit
// vehicle stream needs Flush to push events to the browser.
func (w *noCacheWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped ResponseWriter so http.NewResponseController
// can reach the underlying connection (used by the SSE handler to clear
// the server's write deadline).
func (w *noCacheWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE support.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the wrapped ResponseWriter so helpers like
// http.NewResponseController can reach the underlying connection
// (used by the SSE handler to clear the server's write deadline).
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
