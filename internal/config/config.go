package config

import (
	"fmt"
	"os"
)

// Config holds the settings the service needs at startup.
type Config struct {
	DatabaseURL string // Postgres connection string (env: DATABASE_URL)
	HTTPAddr    string // Address the HTTP server listens on (env: HTTP_ADDR)
}

// Load reads the configuration from environment variables.
//
// DATABASE_URL is required; Load returns an error if it is empty.
// HTTP_ADDR is optional and defaults to ":8000".
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		HTTPAddr:    os.Getenv("HTTP_ADDR"),
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8000"
	}
	return cfg, nil
}
