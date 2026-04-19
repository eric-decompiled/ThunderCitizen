package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/council"
	"thundercitizen/internal/httperr"
)

type stubCouncilStore struct {
	listMeetingSummaries   func(ctx context.Context, f council.MeetingFilter) ([]council.MeetingSummary, int, error)
	councillorVoteStatsAll func(ctx context.Context, term string) (map[string]council.CouncillorVoteStats, error)
	councillorNotableAll   func(ctx context.Context, term string) (map[string][]council.CouncillorNotableVote, error)
	headlineVotes          func(ctx context.Context, term string) ([]council.HeadlineVote, error)
	voteMatrix             func(ctx context.Context, term string) ([]council.VoteMatrixMotion, []council.VoteMatrixRecord, error)
	motionStats            func(ctx context.Context, term string) (int, int, int, error)
	searchMotions          func(ctx context.Context, f council.MotionFilter) ([]council.MotionRow, int, error)
	getMeetingByID         func(ctx context.Context, id string) (*council.MeetingDetail, error)
	getMeetingBySlug       func(ctx context.Context, slug string) (*council.MeetingDetail, error)
	loadVoteRecords        func(ctx context.Context, motionID int64) (*council.VoteRecord, error)
}

func (s stubCouncilStore) ListMeetingSummaries(ctx context.Context, f council.MeetingFilter) ([]council.MeetingSummary, int, error) {
	return s.listMeetingSummaries(ctx, f)
}
func (s stubCouncilStore) CouncillorVoteStatsAll(ctx context.Context, term string) (map[string]council.CouncillorVoteStats, error) {
	return s.councillorVoteStatsAll(ctx, term)
}
func (s stubCouncilStore) CouncillorNotableVotesAll(ctx context.Context, term string) (map[string][]council.CouncillorNotableVote, error) {
	return s.councillorNotableAll(ctx, term)
}
func (s stubCouncilStore) HeadlineVotes(ctx context.Context, term string) ([]council.HeadlineVote, error) {
	return s.headlineVotes(ctx, term)
}
func (s stubCouncilStore) VoteMatrix(ctx context.Context, term string) ([]council.VoteMatrixMotion, []council.VoteMatrixRecord, error) {
	return s.voteMatrix(ctx, term)
}
func (s stubCouncilStore) MotionStats(ctx context.Context, term string) (int, int, int, error) {
	return s.motionStats(ctx, term)
}
func (s stubCouncilStore) SearchMotions(ctx context.Context, f council.MotionFilter) ([]council.MotionRow, int, error) {
	return s.searchMotions(ctx, f)
}
func (s stubCouncilStore) GetMeetingByID(ctx context.Context, id string) (*council.MeetingDetail, error) {
	return s.getMeetingByID(ctx, id)
}
func (s stubCouncilStore) GetMeetingBySlug(ctx context.Context, slug string) (*council.MeetingDetail, error) {
	if s.getMeetingBySlug != nil {
		return s.getMeetingBySlug(ctx, slug)
	}
	// Default: tests target the UUID/ID path — return ErrNoRows so the
	// CouncilMeeting handler falls through to GetMeetingByID.
	return nil, pgx.ErrNoRows
}
func (s stubCouncilStore) LoadVoteRecords(ctx context.Context, motionID int64) (*council.VoteRecord, error) {
	return s.loadVoteRecords(ctx, motionID)
}
func (s stubCouncilStore) MeetingIDsByDates(ctx context.Context, dates []string) (map[string]string, error) {
	return nil, nil
}

func assertInternalError(t *testing.T, rr *httptest.ResponseRecorder) {
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

func TestCouncillorsReturnsInternalErrorWhenVoteMatrixFails(t *testing.T) {
	orig := newCouncilStore
	t.Cleanup(func() { newCouncilStore = orig })

	newCouncilStore = func(_ *pgxpool.Pool) councilStore {
		return stubCouncilStore{
			councillorVoteStatsAll: func(context.Context, string) (map[string]council.CouncillorVoteStats, error) {
				return map[string]council.CouncillorVoteStats{}, nil
			},
			councillorNotableAll: func(context.Context, string) (map[string][]council.CouncillorNotableVote, error) {
				return map[string][]council.CouncillorNotableVote{}, nil
			},
			headlineVotes: func(context.Context, string) ([]council.HeadlineVote, error) { return nil, nil },
			voteMatrix: func(context.Context, string) ([]council.VoteMatrixMotion, []council.VoteMatrixRecord, error) {
				return nil, nil, errors.New("vote matrix failed")
			},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/councillors", nil)
	rr := httptest.NewRecorder()

	(&Handlers{}).Councillors(rr, req)

	assertInternalError(t, rr)
}

func TestCouncilReturnsInternalErrorWhenMotionStatsFails(t *testing.T) {
	orig := newCouncilStore
	t.Cleanup(func() { newCouncilStore = orig })

	newCouncilStore = func(_ *pgxpool.Pool) councilStore {
		return stubCouncilStore{
			listMeetingSummaries: func(context.Context, council.MeetingFilter) ([]council.MeetingSummary, int, error) {
				return nil, 0, nil
			},
			motionStats: func(context.Context, string) (int, int, int, error) {
				return 0, 0, 0, errors.New("motion stats failed")
			},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/minutes", nil)
	rr := httptest.NewRecorder()

	(&Handlers{}).Council(rr, req)

	assertInternalError(t, rr)
}

func TestCouncilMeetingReturnsInternalErrorWhenVoteRecordsFail(t *testing.T) {
	orig := newCouncilStore
	t.Cleanup(func() { newCouncilStore = orig })

	newCouncilStore = func(_ *pgxpool.Pool) councilStore {
		return stubCouncilStore{
			// Resolve the slug directly so the handler skips the
			// slug-to-ID fallback redirect and proceeds to load
			// vote records — which fails, triggering the 500 we
			// want to assert on.
			getMeetingBySlug: func(context.Context, string) (*council.MeetingDetail, error) {
				return &council.MeetingDetail{
					ID: "m1",
					Motions: []council.MotionRow{
						{ID: 42, YeaCount: 1},
					},
				}, nil
			},
			loadVoteRecords: func(context.Context, int64) (*council.VoteRecord, error) {
				return nil, errors.New("vote records failed")
			},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/minutes/m1", nil)
	req.SetPathValue("id", "m1")
	rr := httptest.NewRecorder()

	(&Handlers{}).CouncilMeeting(rr, req)

	assertInternalError(t, rr)
}
