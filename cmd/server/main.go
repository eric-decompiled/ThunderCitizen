package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/go-chi/chi/v5"

	"thundercitizen/internal/assets"
	"thundercitizen/internal/cache"
	"thundercitizen/internal/config"
	"thundercitizen/internal/database"
	"thundercitizen/internal/handlers"
	"thundercitizen/internal/logger"
	"thundercitizen/internal/middleware"
	"thundercitizen/internal/muni"
	"thundercitizen/internal/munisign"
	"thundercitizen/internal/transit"
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

	// Apply signed municipal data bundle. In production, downloads
	// muni.zip from DO Spaces, verifies the SSH signature, and loads
	// TSV data into the database. In dev, reads local muni/ directory.
	// Each dataset is tracked in data_patch_log with the signer's
	// fingerprint — skip if already applied with the same sha256.
	muniCtx, muniCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if bundle, err := resolveMuniBundle(muniCtx, cfg); err != nil {
		log.Error("muni data unavailable", "err", err)
	} else if n, err := muni.Apply(muniCtx, db, bundle); err != nil {
		log.Error("muni data apply failed", "err", err)
	} else if n > 0 {
		log.Info("applied muni data", "datasets", n)
	}
	muniCancel()

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
	})
	th.VehicleStream.Start(transitCtx)

	h := handlers.New(db)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.SecureHeaders)
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
		r.Get("/councillors/{slug}", h.CouncillorProfile)
		r.Get("/minutes", h.Council)
		r.Get("/minutes/{id}", h.CouncilMeeting)
		r.Get("/motions", h.Motions)
		r.Get("/about", h.About)
		r.Get("/docs/signing", h.SigningGuide)
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

// resolveMuniBundle downloads and verifies the signed muni bundle.
// In dev, reads local muni/ directory without signature verification.
func resolveMuniBundle(ctx context.Context, cfg *config.Config) (*muni.Bundle, error) {
	// Dev mode: local directory, no verification.
	if cfg.Environment != "production" && cfg.MuniURL == config.DataBaseURL+"/index.json" {
		return &muni.Bundle{FS: os.DirFS("data/muni")}, nil
	}
	pubKey := resolvePubKey()
	log.Info("downloading muni data", "url", cfg.MuniURL)
	return muni.Load(ctx, cfg.MuniURL, pubKey)
}

func resolvePubKey() []byte {
	if k := os.Getenv("MUNISIGN_KEY"); k != "" {
		return []byte(k)
	}
	if munisign.SigningKey != "" {
		return []byte(munisign.SigningKey)
	}
	return nil
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
