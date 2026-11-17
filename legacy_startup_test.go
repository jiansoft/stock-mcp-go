package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stockmcp/config"
)

func TestClosedLegacyDoesNotOpenSQLite(t *testing.T) {
	for _, expired := range []bool{false, true} {
		cfg := &config.Config{LegacySQLiteFallback: expired, APIKeyDBPath: filepath.Join(t.TempDir(), "missing", "keys.db")}
		if expired {
			cfg.LegacySQLiteUntil = time.Now().Add(-time.Hour)
		}
		keys, closeKeys, err := openLegacyKeys(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil || keys != nil {
			t.Fatalf("closed legacy opened SQLite: keys=%v err=%v", keys, err)
		}
		closeKeys()
		if _, err := os.Stat(filepath.Dir(cfg.APIKeyDBPath)); !os.IsNotExist(err) {
			t.Fatalf("closed legacy touched data directory: %v", err)
		}
	}
}

func TestOpenLegacyImportsBootstrapKey(t *testing.T) {
	cfg := &config.Config{LegacySQLiteFallback: true, APIKeyDBPath: filepath.Join(t.TempDir(), "keys.db"), APIKey: "legacy-key", APIKeyPepper: "test-pepper-at-least-thirty-two-bytes"}
	keys, closeKeys, err := openLegacyKeys(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer closeKeys()
	if _, ok := keys.Authenticate(t.Context(), cfg.APIKey); !ok {
		t.Fatal("enabled legacy bootstrap key rejected")
	}
}
