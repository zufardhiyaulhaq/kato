// Package config loads kato runtime settings from environment variables
// (timeouts, Run TTL, concurrency, evidence caps; spec §11). LLM settings live
// in ModelConfig CRs, not here.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Namespace     string        // kato's own namespace (for Runs + Secrets)
	ListenAddr    string        // e.g. ":8080"
	StepTimeout   time.Duration
	RunTTL        time.Duration
	MaxConcurrent int
	GCInterval    time.Duration
}

func Load() Config {
	return Config{
		Namespace:     getEnv("KATO_NAMESPACE", "kato"),
		ListenAddr:    getEnv("KATO_LISTEN_ADDR", ":8080"),
		StepTimeout:   getDuration("KATO_STEP_TIMEOUT", 30*time.Second),
		RunTTL:        getDuration("KATO_RUN_TTL", 7*24*time.Hour),
		MaxConcurrent: getInt("KATO_MAX_CONCURRENT", 10),
		GCInterval:    getDuration("KATO_GC_INTERVAL", time.Hour),
	}
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func getInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
