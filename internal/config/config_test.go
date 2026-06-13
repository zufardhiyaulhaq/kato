package config

import (
	"testing"
	"time"
)

func TestLoadRunReconcilerDefaults(t *testing.T) {
	t.Setenv("KATO_RUN_RECONCILE_CONCURRENCY", "")
	t.Setenv("KATO_RUN_MAX_DURATION", "")
	cfg := Load()
	if cfg.RunReconcileConcurrency != 2 {
		t.Errorf("RunReconcileConcurrency default = %d, want 2", cfg.RunReconcileConcurrency)
	}
	if cfg.RunMaxDuration != time.Hour {
		t.Errorf("RunMaxDuration default = %s, want 1h", cfg.RunMaxDuration)
	}
}

func TestLoadRunReconcilerOverrides(t *testing.T) {
	t.Setenv("KATO_RUN_RECONCILE_CONCURRENCY", "5")
	t.Setenv("KATO_RUN_MAX_DURATION", "30m")
	cfg := Load()
	if cfg.RunReconcileConcurrency != 5 {
		t.Errorf("RunReconcileConcurrency = %d, want 5", cfg.RunReconcileConcurrency)
	}
	if cfg.RunMaxDuration != 30*time.Minute {
		t.Errorf("RunMaxDuration = %s, want 30m", cfg.RunMaxDuration)
	}
}
