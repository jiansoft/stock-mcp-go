package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"stockmcp/apikey"
)

type memberRateTestAuth map[string]int64

func (a memberRateTestAuth) Authenticate(_ context.Context, token string) (apikey.Principal, bool) {
	member, ok := a[token]
	return apikey.Principal{MemberID: member, KeyID: token}, ok
}

func TestMemberRequestRateSharesIdentityAcrossTokensAndAddresses(t *testing.T) {
	cfg := testConfig()
	cfg.MemberRequestsPerMinute = 1
	cfg.MemberRequestBurst = 1
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	auth := memberRateTestAuth{"smcp_first": 1, "smcp_second": 1, "smcp_other": 2, "smcp_unbound": 0}
	h := NewHandlerWithAPIKeys(cfg, logger, mcp, nil, nil, WithAuthenticator(auth))
	for _, test := range []struct {
		token, addr string
		status      int
	}{
		{"smcp_first", "192.0.2.1:8111", http.StatusNoContent},
		{"smcp_second", "192.0.2.2:8111", http.StatusTooManyRequests},
		{"smcp_other", "192.0.2.2:8111", http.StatusNoContent},
		{"smcp_unbound", "192.0.2.1:8111", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.RemoteAddr = test.addr
		req.Header.Set("Authorization", "Bearer "+test.token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("%s：得到 %d，預期 %d", test.token, rec.Code, test.status)
		}
		if test.status == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
			t.Fatal("限流應告知等待秒數")
		}
	}
}
