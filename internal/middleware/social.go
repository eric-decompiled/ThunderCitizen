package middleware

import (
	"context"
	"net/http"
)

type socialCtxKey int

const (
	ctxBaseURL socialCtxKey = iota
	ctxPath
)

// Social stashes the canonical base URL and the current request path on
// the request context so templ templates can build absolute URLs for
// og:url / og:image without threading them through every view model.
func Social(baseURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ctxBaseURL, baseURL)
			ctx = context.WithValue(ctx, ctxPath, r.URL.Path)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func BaseURLFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxBaseURL).(string); ok {
		return v
	}
	return ""
}

func PathFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxPath).(string); ok {
		return v
	}
	return ""
}
