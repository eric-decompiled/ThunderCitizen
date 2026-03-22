package transit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"thundercitizen/internal/httperr"
)

func routeRequest(method, target, param string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", param)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func assertTransitInternalError(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rr.Code)
	}
	var resp httperr.Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "internal server error" {
		t.Fatalf("expected generic internal error, got %q", resp.Error)
	}
}

func testHandler(getRoute func(context.Context, string) (*RouteInfo, error), tpLoader func(context.Context, string, time.Time) ([]TimepointSchedule, error)) *Handler {
	svc := &Service{
		getRoute:             getRoute,
		routeTimepointLoader: tpLoader,
	}
	return &Handler{svc: svc}
}

func TestRoutePageReturnsInternalErrorWhenScheduleBodyLoadFails(t *testing.T) {
	h := testHandler(
		func(context.Context, string) (*RouteInfo, error) {
			return &RouteInfo{RouteID: "1"}, nil
		},
		func(context.Context, string, time.Time) ([]TimepointSchedule, error) {
			return nil, errors.New("schedule failed")
		},
	)

	req := routeRequest(http.MethodGet, "/transit/route/1?partial=schedule-body", "1")
	rr := httptest.NewRecorder()

	h.routePage(rr, req)

	assertTransitInternalError(t, rr)
}

func TestRoutePageReturnsInternalErrorWhenSchedulePartialLoadFails(t *testing.T) {
	h := testHandler(
		func(context.Context, string) (*RouteInfo, error) {
			return &RouteInfo{RouteID: "1", ShortName: "1"}, nil
		},
		func(context.Context, string, time.Time) ([]TimepointSchedule, error) {
			return nil, errors.New("schedule failed")
		},
	)

	req := routeRequest(http.MethodGet, "/transit/route/1?partial=schedule", "1")
	rr := httptest.NewRecorder()

	h.routePage(rr, req)

	assertTransitInternalError(t, rr)
}
