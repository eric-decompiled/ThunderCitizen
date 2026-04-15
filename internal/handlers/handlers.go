package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/budget"
	"thundercitizen/internal/cache"
	"thundercitizen/internal/council"
	"thundercitizen/internal/data"
	"thundercitizen/internal/database"
	"thundercitizen/internal/httperr"
	"thundercitizen/internal/logger"
	"thundercitizen/internal/views"
	"thundercitizen/templates/pages"
)

var log = logger.New("handlers")

// Version info set via ldflags at build time.
var (
	Commit    = "dev"
	BuildTime = "unknown"
)

type Handlers struct {
	db     *pgxpool.Pool
	ledger *budget.Ledger
}

type councilStore interface {
	ListMeetingSummaries(ctx context.Context, f council.MeetingFilter) ([]council.MeetingSummary, int, error)
	CouncillorVoteStatsAll(ctx context.Context, term string) (map[string]council.CouncillorVoteStats, error)
	CouncillorNotableVotesAll(ctx context.Context, term string) (map[string][]council.CouncillorNotableVote, error)
	HeadlineVotes(ctx context.Context, term string) ([]council.HeadlineVote, error)
	VoteMatrix(ctx context.Context, term string) ([]council.VoteMatrixMotion, []council.VoteMatrixRecord, error)
	CouncillorVotingRecord(ctx context.Context, councillor, term string) ([]council.CouncillorVoteRow, error)
	MotionStats(ctx context.Context, term string) (int, int, int, error)
	SearchMotions(ctx context.Context, f council.MotionFilter) ([]council.MotionRow, int, error)
	GetMeetingByID(ctx context.Context, id string) (*council.MeetingDetail, error)
	LoadVoteRecords(ctx context.Context, motionID int64) (*council.VoteRecord, error)
	MeetingIDsByDates(ctx context.Context, dates []string) (map[string]string, error)
}

var newCouncilStore = func(db *pgxpool.Pool) councilStore {
	return council.NewStore(db)
}

func New(db *pgxpool.Pool) *Handlers {
	return &Handlers{db: db, ledger: budget.NewLedger(db)}
}

// renderPage sends the HTMX partial if the request is an HX-Request, otherwise the full page.
//
// Default Cache-Control is cache.Page (5 minutes) — generous enough that
// browsers don't hammer us on back-navigation or refresh spam, short enough
// that content updates propagate within a coffee break. Handlers that render
// live or rapidly-changing content should override the header before calling
// this. See internal/cache for the full strategy list.
func renderPage(w http.ResponseWriter, r *http.Request, partial, full templ.Component) {
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", cache.Page)
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Push-Url", r.URL.String())
		partial.Render(r.Context(), w)
		return
	}
	full.Render(r.Context(), w)
}

// pageOffset parses a "page" query param and returns the SQL offset for the given limit.
func pageOffset(r *http.Request, limit int) int {
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		return (p - 1) * limit
	}
	return 0
}

func (h *Handlers) Home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		h.NotFound(w, r)
		return
	}

	store := newCouncilStore(h.db)
	recent, _, err := store.ListMeetingSummaries(r.Context(), council.MeetingFilter{
		Term:  "2022-2026",
		Limit: 3,
	})
	if err != nil {
		log.Warn("failed to load recent meetings", "err", err)
	}

	vm := views.NewHomeViewModel(recent)
	pages.Home(vm).Render(r.Context(), w)
}

func (h *Handlers) Budget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vm := views.NewBudgetViewModel(views.DefaultBudgetYear, ctx, h.ledger)
	renderPage(w, r, pages.BudgetPartial(vm), pages.Budget(vm))
}

func (h *Handlers) Councillors(w http.ResponseWriter, r *http.Request) {
	store := newCouncilStore(h.db)
	ctx := r.Context()

	// Parse term from query param, default to current term
	termYear := data.DefaultTerm
	if q := r.URL.Query().Get("term"); q != "" {
		if y, err := strconv.Atoi(q); err == nil {
			if _, ok := data.CouncilByTerm[y]; ok {
				termYear = y
			}
		}
	}

	// Fetch vote data only for current term (older terms not yet verified)
	var vd views.TermVoteData
	if termYear == data.DefaultTerm {
		term := data.TermRange(termYear)
		vs, err := store.CouncillorVoteStatsAll(ctx, term)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		nv, err := store.CouncillorNotableVotesAll(ctx, term)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		hv, err := store.HeadlineVotes(ctx, term)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		mm, mr, err := store.VoteMatrix(ctx, term)
		if err != nil {
			httperr.Internal(w, err)
			return
		}
		vd = views.TermVoteData{
			VoteStats:     vs,
			NotableVotes:  nv,
			HeadlineVotes: hv,
			MatrixMotions: mm,
			MatrixRecords: mr,
		}
	}

	vm := views.NewCouncillorsViewModel(termYear, vd)

	renderPage(w, r, pages.CouncillorsPartial(vm), pages.Councillors(vm))
}

func (h *Handlers) CouncillorProfile(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		httperr.BadRequest(w, "missing councillor")
		return
	}

	c, termYear, found := data.FindCouncillorBySlug(slug)
	if !found {
		h.NotFound(w, r)
		return
	}

	term := data.TermRange(termYear)
	store := newCouncilStore(h.db)
	ctx := r.Context()

	voteStats, err := store.CouncillorVoteStatsAll(ctx, term)
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	notableVotes, err := store.CouncillorNotableVotesAll(ctx, term)
	if err != nil {
		httperr.Internal(w, err)
		return
	}
	voteRecord, err := store.CouncillorVotingRecord(ctx, c.Name, term)
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	vm := views.NewCouncillorPageViewModel(c, termYear, voteStats, notableVotes, voteRecord)
	pages.CouncillorProfile(vm).Render(ctx, w)
}

func (h *Handlers) About(w http.ResponseWriter, r *http.Request) {
	pages.About().Render(r.Context(), w)
}

// NotFound renders the themed 404 page with a 404 status. Used for HTML
// page routes where a plain httperr JSON response would break the theme.
// API routes should continue to use httperr.NotFound (JSON).
func (h *Handlers) NotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cache.Page)
	w.WriteHeader(http.StatusNotFound)
	vm := views.NewNotFoundViewModel(r.Method, r.URL.Path)
	pages.NotFound(vm).Render(r.Context(), w)
}

func (h *Handlers) Council(w http.ResponseWriter, r *http.Request) {
	store := newCouncilStore(h.db)

	term := "2022-2026"
	// 2018-2022 term was dropped; only the current term is supported.
	if t := r.URL.Query().Get("term"); t == "2022" {
		term = "2022-2026"
	}

	filter := council.MeetingFilter{
		Term:          term,
		Query:         r.URL.Query().Get("q"),
		RecordedVotes: r.URL.Query().Get("votes") == "1",
		Defeated:      r.URL.Query().Get("defeated") == "1",
		Limit:         25,
	}
	filter.Offset = pageOffset(r, filter.Limit)

	meetings, total, err := store.ListMeetingSummaries(r.Context(), filter)
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	meetingCount, totalMotions, recordedVotes, err := store.MotionStats(r.Context(), filter.Term)
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	vm := views.NewCouncilViewModel(meetings, total, [3]int{meetingCount, totalMotions, recordedVotes}, filter)

	// If there's a search query, return motion search results instead
	if q := r.URL.Query().Get("q"); q != "" {
		mFilter := council.MotionFilter{
			Query: q,
			Term:  term,
			Limit: 25,
		}
		mFilter.Offset = pageOffset(r, mFilter.Limit)
		motions, mTotal, mErr := store.SearchMotions(r.Context(), mFilter)
		if mErr != nil {
			httperr.Internal(w, mErr)
			return
		}
		mvm := views.NewMotionSearchViewModel(motions, mTotal, mFilter)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Push-Url", r.URL.String())
			pages.MotionSearchResults(mvm).Render(r.Context(), w)
			return
		}
		vm.MotionSearch = &mvm
		pages.Council(vm).Render(r.Context(), w)
		return
	}

	renderPage(w, r, pages.CouncilTablePartial(vm), pages.Council(vm))
}

func (h *Handlers) Motions(w http.ResponseWriter, r *http.Request) {
	dest := "/minutes"
	if q := r.URL.RawQuery; q != "" {
		dest += "?" + q
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (h *Handlers) CouncilMeeting(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httperr.BadRequest(w, "missing meeting ID")
		return
	}

	store := newCouncilStore(h.db)
	md, err := store.GetMeetingByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		h.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, err)
		return
	}

	// Load vote records for motions that have them
	for i := range md.Motions {
		if md.Motions[i].YeaCount > 0 || md.Motions[i].NayCount > 0 {
			vr, err := store.LoadVoteRecords(r.Context(), md.Motions[i].ID)
			if err != nil {
				httperr.Internal(w, err)
				return
			}
			md.Motions[i].Votes = vr
		}
	}

	vm := views.NewMeetingViewModel(md)
	pages.CouncilMeeting(vm).Render(r.Context(), w)
}

func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"commit":     Commit,
		"build_time": BuildTime,
	})
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	if err := database.HealthCheck(context.Background(), h.db); err != nil {
		// Plain text on failure — Docker's wget healthcheck greps for
		// "OK", which this deliberately does not match.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("NOT OK: database unhealthy\n"))
		log.Error("health db check failed")
		return
	}

	// Content-negotiate three shapes:
	//   application/json → {"status":"OK"} for machine clients that
	//                      want to parse rather than grep
	//   text/html        → themed dashboard for browsers
	//   anything else    → plain "OK" for Docker's wget probe (sends
	//                      Accept: */* by default) — keeps the
	//                      healthcheck grep-match intact
	accept := r.Header.Get("Accept")
	switch {
	case strings.Contains(accept, "application/json"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"OK"}`))
	case strings.Contains(accept, "text/html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", cache.Live)
		vm := views.NewHealthViewModel(
			valueOr(os.Getenv("TC_IMAGE"), "local-dev"),
			Commit,
			BuildTime,
		)
		pages.Health(vm).Render(r.Context(), w)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
