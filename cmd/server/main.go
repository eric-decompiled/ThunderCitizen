package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"thundercitizen/internal/assets"
	"thundercitizen/internal/cache"
	"thundercitizen/internal/config"
	"thundercitizen/internal/database"
	"thundercitizen/internal/handlers"
	"thundercitizen/internal/logger"
	"thundercitizen/internal/metrics"
	"thundercitizen/internal/middleware"
	"thundercitizen/internal/muni"
	"thundercitizen/internal/munisign"
	"thundercitizen/internal/transit"
	"thundercitizen/internal/views"
	"thundercitizen/templates/pages"
)

var log = logger.New("server")

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Info("connecting to database")
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	log.Info("database connected")

	log.Info("running migrations")
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		log.Warn("migration warning", "err", err)
	}

	// Trust store is loaded once at startup. A malformed or
	// ambiguous trust store is a deploy-blocking bug — we refuse
	// to serve a single request without a clean trust graph.
	trust, err := munisign.LoadTrust()
	if err != nil {
		log.Error("trust store failed to load", "err", err)
		os.Exit(1)
	}
	log.Info("trust store",
		"approved", len(trust.Approved),
		"revoked", len(trust.Revoked))

	// Muni apply runs asynchronously in production so the HTTP
	// listener comes up immediately. In dev we fast-path the whole
	// thing when BOD.tsv is unchanged since the last boot — no
	// network, no Postgres staging scan, instant hot-reload.
	muniStatus := muni.NewStatus()
	go applyMuniBundle(db, cfg, trust, muniStatus)

	// GTFS: synchronous initial fetch so routes are in the DB before the
	// server starts serving requests. On first boot this downloads +
	// extracts + loads (~30s). On subsequent boots it hash-compares
	// against the version stored in the DB and short-circuits (~2s)
	// since the routes are still in the persistent volume.
	//
	// On failure we log and continue — the background refresher below
	// will retry on its next tick, and the DB still has whatever was
	// loaded last time. Better to serve stale data than to fail to boot.
	if err := transit.EnsureStaticGTFS(); err != nil {
		log.Warn("GTFS dir setup failed", "err", err)
	}
	gtfsRefresher := transit.NewGTFSRefresher(db)
	gtfsCtx, gtfsCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	if err := gtfsRefresher.CheckAndReload(gtfsCtx); err != nil {
		log.Warn("initial GTFS fetch failed; background refresher will retry", "err", err)
	}
	gtfsCancel()

	// Start transit recorder and stats engine
	transitCtx, transitCancel := context.WithCancel(context.Background())
	defer transitCancel()

	recorder := transit.NewRecorder(db)
	recorder.Start(transitCtx)

	// Keep transit.route_band_chunk populated without operator action:
	// a startup backfill catches any missing recent days, then every 10
	// minutes today's chunks are rebuilt so /transit/metrics stays fresh
	// as bands close.
	transit.NewChunkRollup(db).Start(transitCtx)

	// Start GTFS background refresher for periodic updates. The initial
	// fetch is already done synchronously above; this loop handles the
	// every-4-hours upstream change detection.
	gtfsRefresher.Start(transitCtx)

	th := transit.NewHandler(db, transit.Renderer{
		TransitLive: func(vm transit.LiveViewModel) transit.RenderFunc {
			return pages.TransitLive(vm).Render
		},
		TransitMetrics: func(vm transit.MetricsViewModel) transit.RenderFunc {
			return pages.TransitMetrics(vm).Render
		},
		TransitRoutes: func(vm transit.RoutesViewModel) transit.RenderFunc {
			return pages.TransitRoutes(vm).Render
		},
		TransitMethod: func(vm transit.MethodViewModel) transit.RenderFunc {
			return pages.TransitMethod(vm).Render
		},
		Route: func(vm transit.RouteViewModel) transit.RenderFunc {
			return pages.Route(vm).Render
		},
		RoutePartial: func(vm transit.RouteViewModel) transit.RenderFunc {
			return pages.RoutePartial(vm).Render
		},
		RouteSchedulePartial: func(vm transit.RouteViewModel) transit.RenderFunc {
			return pages.RouteSchedulePartial(vm).Render
		},
		RouteScheduleTodayPartial: func(vm transit.RouteViewModel) transit.RenderFunc {
			return pages.RouteScheduleTodayPartial(vm).Render
		},
		RouteScheduleBodyPartial: func(vm transit.RouteViewModel) transit.RenderFunc {
			return pages.RouteScheduleBodyPartial(vm).Render
		},
		AuditIndex: func(vm transit.AuditIndexViewModel) transit.RenderFunc {
			return pages.AuditIndex(vm).Render
		},
		AuditRoute: func(vm transit.AuditRouteViewModel) transit.RenderFunc {
			return pages.AuditRoute(vm).Render
		},
		PlanPartial: func(plan *transit.PlanResult, summary bool, fromLat, fromLon, toLat, toLon float64) transit.RenderFunc {
			return pages.PlanResults(pages.PlanPartialViewModel{
				Plan: plan, Summary: summary,
				FromLat: fromLat, FromLon: fromLon,
				ToLat: toLat, ToLon: toLon,
			}).Render
		},
	}, recorder)
	th.VehicleStream.Start(transitCtx)

	h := handlers.New(db, recorder)
	h.AttachMuni(trust, muniStatus)

	r := chi.NewRouter()
	// RequestID runs outermost so the per-request logger it attaches to
	// the context is visible to every downstream middleware and handler,
	// including Recoverer's panic log and RequestLogger's completion log.
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.SecureHeaders)
	// Metrics counter sits between RequestID and RequestLogger. It reads
	// the matched chi route pattern after next.ServeHTTP returns, so it
	// feeds the /health page's in-memory route histogram with properly
	// normalized buckets (/minutes/{id}, not /minutes/2026-03-17).
	r.Use(metrics.Middleware)
	r.Use(middleware.RequestLogger)
	// In dev (and any non-"production" ENVIRONMENT), neutralize every
	// Cache-Control header set by inner handlers so refreshing the dev
	// browser always shows the latest work. In production this is a
	// no-op — handlers' max-age values pass through untouched.
	r.Use(middleware.NoCacheInDev(cfg.Environment))

	// Static files — councillor photos, JS/CSS, PMTiles basemap, budget
	// JSON, etc. Served with a week-long immutable Cache-Control. Cache
	// invalidation across deploys relies on the assets fingerprinter:
	// every file is hashed at boot and templates emit "?v=<hash>" so
	// changed files request a brand-new URL the browser has never seen.
	if err := assets.Init("static", "/static"); err != nil {
		log.Warn("asset fingerprint scan failed; serving without cache busters", "err", err)
	}
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", cache.Static)
		staticFS.ServeHTTP(w, req)
	}))

	// App routes. Grouped under a PageCache middleware so every top-level
	// page sends a sensible default Cache-Control. Handlers can override
	// by setting their own header (e.g. for pages that must revalidate).
	r.Group(func(r chi.Router) {
		r.Use(middleware.PageCache(cache.Page))
		r.Get("/", h.Home)
		r.Get("/budget", h.Budget)
		r.Get("/councillors", h.Councillors)
		r.Get("/minutes", h.Council)
		r.Get("/minutes/{id}", h.CouncilMeeting)
		r.Get("/motions", h.Motions)
		r.Get("/about", h.About)
		r.Get("/data", h.DataPacks)
	})
	// Accept HEAD on /health too — Docker's wget --spider probe uses HEAD
	// and was getting a 405 from a GET-only route, which made the container
	// look permanently unhealthy. Go's stdlib strips the body on HEAD
	// automatically, so the same handler works for both.
	r.Get("/health", h.Health)
	r.Head("/health", h.Health)
	r.Get("/version", h.Version)

	// Transit — self-contained page + API routes
	r.Mount("/transit", th.PageRoutes())
	r.Mount("/api/transit", th.APIRoutes())

	// Catch-all for unmatched top-level paths. /api/transit/* misses land
	// in the mounted sub-router's own default 404 (plain text), so API
	// clients continue to get a non-HTML response.
	r.NotFound(h.NotFound)

	// Build the 404 page's route registry by walking the chi router.
	// Only GET routes that a human could navigate to — no API prefixes,
	// no static assets, no health probes, no parameterized paths. Done
	// once here so the 404 handler doesn't re-walk per request.
	var registryPaths []string
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet {
			return nil
		}
		if strings.ContainsAny(route, "{*") {
			return nil
		}
		if strings.HasPrefix(route, "/api/") ||
			strings.HasPrefix(route, "/static/") ||
			route == "/health" ||
			route == "/version" {
			return nil
		}
		registryPaths = append(registryPaths, route)
		return nil
	})
	views.SetNotFoundRegistry(registryPaths)

	// Boot time drives the /health page's uptime readout. Set it right
	// before ListenAndServe so database migrations and other startup
	// work don't count against uptime.
	metrics.SetBootTime(time.Now())

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("starting", "port", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("shutting down")
	transitCancel() // stop transit recorder and stats engine

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error("shutdown failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// muniCheckInterval is how long a successful bundle check is valid before
// we'll re-hit the upstream source. Kept short enough to pick up new
// publications within a day, long enough that dev hot-reloads don't spam DO.
const muniCheckInterval = 24 * time.Hour

// applyMuniBundle resolves the bundle (local dir in dev, DO Spaces in
// prod) and applies it. Runs in its own goroutine — the HTTP listener
// is already accepting requests from the last-applied DB state when
// this starts. Status updates flow through the shared muni.Status so
// the /data admin page can render progress and errors.
func applyMuniBundle(db *pgxpool.Pool, cfg *config.Config, trust *munisign.Trust, status *muni.Status) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	localDev := cfg.Environment != "production" && cfg.MuniURL == config.DataBaseURL+"/index.json"

	if localDev {
		// Dev fast path: if BOD.tsv hash matches the last-applied
		// hash in muni_dev_cache we can skip the whole apply loop.
		// Catches the common `make dev` hot-reload case where the
		// operator just tweaked a .go file — no need to reparse,
		// rehash, and query data_patch_log for every dataset.
		bodSha, err := hashLocalBOD()
		if err != nil {
			status.SetError(err)
			log.Error("dev: BOD.tsv hash failed", "err", err)
			return
		}
		cached, err := muni.DevCacheBODSha(ctx, db)
		if err != nil {
			log.Warn("dev cache read failed; applying anyway", "err", err)
		} else if cached != "" && cached == bodSha {
			status.SetSkipped("dev fast-path: BOD unchanged since last boot")
			log.Info("muni: dev fast-path, BOD unchanged", "sha", bodSha[:12])
			return
		}

		status.SetState(muni.StateApplying, "dev: applying local bundle")
		bundle := &muni.Bundle{FS: os.DirFS("data/muni")}
		n, err := muni.Apply(ctx, db, bundle)
		if err != nil {
			status.SetError(err)
			log.Error("muni data apply failed", "err", err)
			return
		}
		if err := muni.SetDevCacheBODSha(ctx, db, bodSha); err != nil {
			log.Warn("failed to update dev cache", "err", err)
		}
		status.SetSuccess("", "", "", n)
		if n > 0 {
			log.Info("applied muni data", "datasets", n)
		}
		return
	}

	// Production path.
	if last, err := muni.LastCheckedAt(ctx, db); err != nil {
		log.Warn("muni fetch state unreadable; fetching anyway", "err", err)
	} else if !last.IsZero() && time.Since(last) < muniCheckInterval {
		status.SetSkipped("prod: last checked within 24h")
		log.Info("muni bundle recently checked; skipping fetch",
			"last_checked", last.Format(time.RFC3339),
			"age", time.Since(last).Round(time.Minute))
		return
	}

	status.SetState(muni.StateFetching, "downloading bundle")
	log.Info("downloading muni data", "url", cfg.MuniURL)

	bundle, err := muni.LoadWithTrust(ctx, cfg.MuniURL, trust)
	if err != nil {
		status.SetError(err)
		log.Error("muni data unavailable", "err", err)
		return
	}

	status.SetState(muni.StateApplying, "applying bundle")
	n, err := muni.Apply(ctx, db, bundle)
	if err != nil {
		status.SetError(err)
		log.Error("muni data apply failed", "err", err)
		return
	}

	signerFP := ""
	signerFile := ""
	merkle := ""
	if bundle.Verification != nil {
		signerFP = bundle.Verification.SignerFingerprint
		merkle = bundle.Verification.MerkleRoot
		if tk, ok := trust.Approved[signerFP]; ok {
			signerFile = tk.Filename
		}
	}
	status.SetSuccess(signerFP, signerFile, merkle, n)

	if n > 0 {
		log.Info("applied muni data", "datasets", n)
	}
	if err := muni.MarkChecked(ctx, db); err != nil {
		log.Warn("failed to record muni check", "err", err)
	}
}

// hashLocalBOD computes SHA-256 over the local BOD.tsv contents. Used
// by the dev fast path to decide whether apply can be skipped.
func hashLocalBOD() (string, error) {
	data, err := os.ReadFile(filepath.Join("data", "muni", muni.BODFile))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runMigrations(databaseURL string) error {
	m, err := migrate.New("file://migrations", databaseURL)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}
