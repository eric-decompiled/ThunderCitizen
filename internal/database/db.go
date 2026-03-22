package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	// Pin every session to Thunder Bay local time. Every date/time the app
	// stores or reports is Eastern — recording, GTFS service days, metric
	// bands, displayed timestamps. Setting the session TZ here means
	// `timestamptz::date` casts and EXTRACT(HOUR FROM …) use local time
	// without every query needing an explicit AT TIME ZONE dance. Any query
	// that wants raw UTC can still cast explicitly.
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["timezone"] = "America/Thunder_Bay"

	// Cache the parameter type descriptions (fast protocol) but don't cache
	// the prepared statement itself — otherwise Postgres switches to a
	// "generic plan" after 5 executions and, for our time-bucketed metric
	// queries, the generic plan picks a pathological join order and the same
	// query that ran in 150ms takes 30+ seconds. Re-planning every call is
	// cheap relative to the actual work the query does, and it keeps the
	// planner honest about the specific parameter values it gets.
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}

func HealthCheck(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return pool.Ping(ctx)
}
