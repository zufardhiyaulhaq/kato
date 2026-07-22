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

// KATO_METHOD_MAX_CONCURRENT configures the direct-method-run limiter,
// independent of KATO_MAX_CONCURRENT; unset falls back to 10.
func TestMethodMaxConcurrent(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		if got := Load().MethodMaxConcurrent; got != 10 {
			t.Errorf("MethodMaxConcurrent = %d, want 10", got)
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("KATO_METHOD_MAX_CONCURRENT", "3")
		if got := Load().MethodMaxConcurrent; got != 3 {
			t.Errorf("MethodMaxConcurrent = %d, want 3", got)
		}
	})
	t.Run("independent of KATO_MAX_CONCURRENT", func(t *testing.T) {
		t.Setenv("KATO_MAX_CONCURRENT", "99")
		if got := Load().MethodMaxConcurrent; got != 10 {
			t.Errorf("MethodMaxConcurrent = %d, want 10 (must not read KATO_MAX_CONCURRENT)", got)
		}
	})
}
