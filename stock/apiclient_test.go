// Package stock 測試 APIClient 對 stock_rust HTTP 契約的狀態碼、認證與 JSON 轉換行為。
package stock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAPIClient 驗證成功回應、404 語意與伺服器錯誤不會被誤當成資料不存在。
func TestAPIClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/stocks/search":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stocks":[{"stock_symbol":"2330","security_code":"2330","name":"台積電"}]}`))
		case "/api/v1/stocks/404/price-history":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/stocks/500/profile":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	stocks, err := client.SearchStock(t.Context(), "2330", 10)
	if err != nil || len(stocks) != 1 || stocks[0].Name != "台積電" {
		t.Fatalf("SearchStock() = %#v, %v", stocks, err)
	}
	if _, err := client.PriceHistory(t.Context(), "404", nil, nil, 10); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("PriceHistory 404 error = %v", err)
	}
	if _, err := client.StockProfile(t.Context(), "500"); err == nil {
		t.Fatal("5xx 不可視為股票不存在")
	}
}

// TestAPIClientAuthentication 驗證每個請求都帶上正確的 Bearer token,且
// 401(金鑰錯誤)不會被誤判成「資料不存在」。
func TestAPIClientAuthentication(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"stocks":[]}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "top-secret", time.Second)
	if _, err := client.SearchStock(t.Context(), "2330", 10); err != nil {
		t.Fatalf("SearchStock() 不應失敗:%v", err)
	}
	if gotAuth != "Bearer top-secret" {
		t.Fatalf("Authorization header 不正確:%q", gotAuth)
	}

	unauthorizedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthorizedServer.Close()
	unauthorizedClient := NewAPIClient(unauthorizedServer.URL, "wrong", time.Second)
	if _, err := unauthorizedClient.LatestDailyQuote(t.Context(), "2330"); err == nil {
		t.Fatal("401 不可視為股票不存在,必須回傳 error")
	} else if errors.Is(err, ErrStockNotFound) {
		t.Fatalf("401 不應被誤判為 ErrStockNotFound:%v", err)
	}
}

// TestAPIClientLatestDailyQuote 驗證 LatestDailyQuote 的成功與 404 兩種路徑。
func TestAPIClientLatestDailyQuote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/latest-quote":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock":{"stock_symbol":"2330","security_code":"2330","name":"台積電"},"quote":{"date":"2026-07-13","closing_price":1000}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	got, err := client.LatestDailyQuote(t.Context(), "2330")
	if err != nil {
		t.Fatalf("LatestDailyQuote() 錯誤:%v", err)
	}
	if got == nil || got.Stock.Name != "台積電" || got.Quote == nil || *got.Quote.ClosingPrice != 1000 {
		t.Fatalf("LatestDailyQuote() = %#v", got)
	}

	notFound, err := client.LatestDailyQuote(t.Context(), "9999")
	if err != nil {
		t.Fatalf("404 不應回傳 error,實際為:%v", err)
	}
	if notFound != nil {
		t.Fatalf("404 應回傳 nil,實際為 %#v", notFound)
	}
}

// TestAPIClientRealtimeSnapshot 驗證 RealtimeSnapshot 的成功與「查無快照」
// (404)兩種路徑——後者由 tools.go 轉成「建議改查日報價」的 tool error。
func TestAPIClientRealtimeSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/realtime-snapshot":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","name":"台積電","price":1000,"source_site":"example.com","updated_at":"2026-07-16T05:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	got, err := client.RealtimeSnapshot(t.Context(), "2330")
	if err != nil {
		t.Fatalf("RealtimeSnapshot() 錯誤:%v", err)
	}
	if got == nil || got.Name != "台積電" || got.Price == nil || *got.Price != 1000 {
		t.Fatalf("RealtimeSnapshot() = %#v", got)
	}

	notFound, err := client.RealtimeSnapshot(t.Context(), "9999")
	if err != nil || notFound != nil {
		t.Fatalf("查無快照應回傳 (nil, nil),實際為 (%#v, %v)", notFound, err)
	}
}
