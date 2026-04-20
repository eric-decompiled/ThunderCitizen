package config

import (
	"os"
	"strings"
	"sync"

	"thundercitizen/internal/logger"
)

var log = logger.New("config")

// DataBaseURL is the default root for runtime data assets (patches, future
// data bundles). Served from DO Spaces + CDN at data.thundercitizen.ca.
const DataBaseURL = "https://thundercitizen.tor1.digitaloceanspaces.com"

type Config struct {
	DatabaseURL string
	Port        string
	Environment string
	PatchesURL  string
	MuniURL     string
	BaseURL     string
}

func Load() *Config {
	return &Config{
		DatabaseURL: Secret("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/thundercitizen?sslmode=disable"),
		Port:        Secret("PORT", "8080"),
		Environment: Secret("ENVIRONMENT", "development"),
		PatchesURL:  Secret("PATCHES_URL", DataBaseURL+"/patches.zip"),
		MuniURL:     Secret("MUNI_URL", DataBaseURL+"/index.json"),
		BaseURL:     strings.TrimRight(Secret("BASE_URL", "http://localhost:8080"), "/"),
	}
}

// Secret reads a value by key: env var wins, then secrets file, then fallback.
// Safe to call from anywhere (cmd/ tools, tests, etc.).
func Secret(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if v, ok := secrets()[key]; ok {
		return v
	}
	return fallback
}

// secrets returns the parsed secrets file, loading it once on first call.
var secrets = sync.OnceValue(func() map[string]string {
	path := os.Getenv("SECRETS_FILE")
	if path == "" {
		path = "secrets.conf"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("failed to read secrets file", "path", path, "err", err)
		}
		return map[string]string{}
	}

	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	log.Info("loaded secrets file", "path", path, "keys", len(m))
	return m
})
