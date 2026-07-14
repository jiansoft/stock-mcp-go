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
