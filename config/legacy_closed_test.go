package config

import (
	"testing"
	"time"
)

func TestClosedLegacyNeedsNoLegacySecrets(t *testing.T) {
	for _, deadline := range []string{"", "2000-01-01"} {
		t.Run(deadline, func(t *testing.T) {
			setRequired(t)
			t.Setenv("MCP_LEGACY_SQLITE_FALLBACK", "false")
			if deadline != "" {
				t.Setenv("MCP_LEGACY_SQLITE_FALLBACK", "true")
				t.Setenv("MCP_LEGACY_SQLITE_UNTIL", deadline)
			}
			t.Setenv("MCP_API_KEY_PEPPER", "")
			t.Setenv("MCP_ADMIN_TOKEN", "")
			if _, err := Load(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyDeadlineBoundary(t *testing.T) {
	deadline := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	cfg := &Config{LegacySQLiteFallback: true, LegacySQLiteUntil: deadline}
	if !cfg.LegacySQLiteOpen(deadline.Add(-time.Nanosecond)) || cfg.LegacySQLiteOpen(deadline) {
		t.Fatal("deadline must close at exact boundary")
	}
}
