package main

import (
	"context"
	"log/slog"
	"time"

	"stockmcp/apikey"
	"stockmcp/config"
)

// openLegacyKeys 關閉或截止後不開啟、建立或匯入 SQLite；資料保留供截止後另行移除程式碼時處理。
func openLegacyKeys(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*apikey.Service, func(), error) {
	if !cfg.LegacySQLiteOpen(time.Now()) {
		return nil, func() {}, nil
	}
	repo, err := apikey.OpenSQLite(ctx, cfg.APIKeyDBPath)
	if err != nil {
		return nil, func() {}, err
	}
	service, err := apikey.NewService(ctx, repo, []byte(cfg.APIKeyPepper), cfg.APIKey, logger)
	if err != nil {
		_ = repo.Close()
		return nil, func() {}, err
	}
	closeLegacy := func() {
		if err := service.Close(); err != nil {
			logger.Error("關閉 API key 服務失敗", "error", err)
		}
		if err := repo.Close(); err != nil {
			logger.Error("關閉 API key 資料庫失敗", "error", err)
		}
	}
	return service, closeLegacy, nil
}
