package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"stockmcp/apikey"
	"stockmcp/config"
)

const (
	adminPagePath    = "/admin/mcp-api-keys"
	adminAPIBasePath = "/api/admin/mcp-api-keys"
	maxAdminBody     = 64 << 10
)

//go:embed admin.html admin.css admin.js
var adminAssets embed.FS

type adminAPI struct {
	keys *apikey.Service
}

func registerAPIKeyAdmin(mux *http.ServeMux, cfg *config.Config, keys *apikey.Service) {
	api := &adminAPI{keys: keys}
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET "+adminAPIBasePath, api.list)
	adminMux.HandleFunc("POST "+adminAPIBasePath, api.create)
	adminMux.HandleFunc("GET "+adminAPIBasePath+"/{id}", api.get)
	adminMux.HandleFunc("PATCH "+adminAPIBasePath+"/{id}", api.update)
	adminMux.HandleFunc("POST "+adminAPIBasePath+"/{id}/enable", api.enable)
	adminMux.HandleFunc("POST "+adminAPIBasePath+"/{id}/disable", api.disable)
	adminMux.HandleFunc("POST "+adminAPIBasePath+"/{id}/rotate", api.rotate)
	adminMux.HandleFunc("DELETE "+adminAPIBasePath+"/{id}", api.delete)

	failures := NewRateLimiter(time.Minute, 10)
	protected := requireAdminToken(
		cfg.AdminToken,
		failures,
		cfg.TrustProxy,
		cfg.TrustedProxyHops,
		noStore(crossOrigin(cfg.TrustedOrigins, limitAdminBody(adminMux))),
	)
	mux.Handle(adminAPIBasePath, protected)
	mux.Handle(adminAPIBasePath+"/", protected)
	mux.HandleFunc("GET "+adminPagePath, serveAdminPage)
	mux.HandleFunc("GET /admin/assets/api-keys.css", serveAdminAsset("admin.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /admin/assets/api-keys.js", serveAdminAsset("admin.js", "text/javascript; charset=utf-8"))
}

func (a *adminAPI) list(w http.ResponseWriter, r *http.Request) {
	items, err := a.keys.List(r.Context())
	if err != nil {
		writeAdminError(w, err)
		return
	}
	if items == nil {
		items = []apikey.APIKey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *adminAPI) get(w http.ResponseWriter, r *http.Request) {
	item, err := a.keys.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type createKeyRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

func (a *adminAPI) create(w http.ResponseWriter, r *http.Request) {
	var input createKeyRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		writeAdminError(w, err)
		return
	}
	item, fullKey, err := a.keys.Create(r.Context(), apikey.CreateInput{
		Name: input.Name, Description: input.Description, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"item": item, "apiKey": fullKey,
		"notice": "完整 API Key 只顯示這一次，關閉後無法再次取得。",
	})
}

type updateKeyRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Version     int64      `json:"version"`
}

func (a *adminAPI) update(w http.ResponseWriter, r *http.Request) {
	var input updateKeyRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		writeAdminError(w, err)
		return
	}
	item, err := a.keys.Update(r.Context(), r.PathValue("id"), apikey.UpdateInput{
		Name: input.Name, Description: input.Description,
		ExpiresAt: input.ExpiresAt, Version: input.Version,
	})
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type versionRequest struct {
	Version int64 `json:"version"`
}

func (a *adminAPI) enable(w http.ResponseWriter, r *http.Request) {
	a.changeStatus(w, r, a.keys.Enable)
}

func (a *adminAPI) disable(w http.ResponseWriter, r *http.Request) {
	a.changeStatus(w, r, a.keys.Disable)
}

func (a *adminAPI) changeStatus(
	w http.ResponseWriter,
	r *http.Request,
	change func(context.Context, string, int64) (apikey.APIKey, error),
) {
	var input versionRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		writeAdminError(w, err)
		return
	}
	item, err := change(r.Context(), r.PathValue("id"), input.Version)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (a *adminAPI) rotate(w http.ResponseWriter, r *http.Request) {
	var input versionRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		writeAdminError(w, err)
		return
	}
	item, fullKey, err := a.keys.Rotate(r.Context(), r.PathValue("id"), input.Version)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item": item, "apiKey": fullKey,
		"notice": "舊 API Key 已立即失效；新 Key 只顯示這一次。",
	})
}

func (a *adminAPI) delete(w http.ResponseWriter, r *http.Request) {
	var input versionRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		writeAdminError(w, err)
		return
	}
	if err := a.keys.Delete(r.Context(), r.PathValue("id"), input.Version); err != nil {
		writeAdminError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeAdminJSON(r *http.Request, dst any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("%w: Content-Type 必須是 application/json", apikey.ErrValidation)
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: JSON 格式不正確", apikey.ErrValidation)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("%w: request body 只能有一個 JSON object", apikey.ErrValidation)
	}
	return nil
}

func writeAdminError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "伺服器無法完成請求"
	switch {
	case errors.Is(err, apikey.ErrValidation):
		status, code, message = http.StatusBadRequest, "validation_error", err.Error()
	case errors.Is(err, apikey.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", apikey.ErrNotFound.Error()
	case errors.Is(err, apikey.ErrConflict), errors.Is(err, apikey.ErrLastActive):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusRequestTimeout, "request_cancelled", "請求已取消或逾時"
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func limitAdminBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxAdminBody {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error": map[string]string{
					"code": "request_too_large", "message": "管理 API request body 上限為 64 KiB",
				},
			})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAdminBody)
		next.ServeHTTP(w, r)
	})
}

func serveAdminPage(w http.ResponseWriter, _ *http.Request) {
	raw, err := adminAssets.ReadFile("admin.html")
	if err != nil {
		http.Error(w, "管理頁面無法載入", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(raw)
}

func serveAdminAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		raw, err := adminAssets.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(raw)
	}
}
