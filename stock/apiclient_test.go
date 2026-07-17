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

// TestAPIClientFinancialHistory 驗證 Phase 1 三個歷史 endpoint 的成功、
// 404(轉為 ErrStockNotFound)、query string 組裝與空陣列語意。
func TestAPIClientFinancialHistory(t *testing.T) {
	// gotQuery 記錄伺服器實際收到的 query string,驗證選填參數「有提供
	// 才會出現、沒提供就不出現」的組裝規則。
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/stocks/2330/monthly-revenues":
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-06","revenues":[{"month":"2026-06","monthly_revenue":263712291,"year_over_year_percent":26.9}]}`))
		case "/api/v1/stocks/2330/financial-statements":
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-Q1","statements":[{"year":2026,"quarter":"Q1","earnings_per_share":13.94,"return_on_equity":8.9}]}`))
		case "/api/v1/stocks/2330/dividends":
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2025-A","dividends":[{"paid_year":2026,"dividend_year":2025,"quarter":"A","cash_dividend":17,"ex_dividend_date":null}]}`))
		case "/api/v1/stocks/4414/monthly-revenues":
			// 已知股票但無資料:200 + 空陣列 + data_as_of null。
			_, _ = w.Write([]byte(`{"stock_symbol":"4414","data_as_of":null,"revenues":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	revenues, err := client.MonthlyRevenueHistory(t.Context(), "2330", RevenueHistoryOptions{From: "2024-01", To: "2026-06", Limit: 24})
	if err != nil || revenues == nil || len(revenues.Revenues) != 1 {
		t.Fatalf("MonthlyRevenueHistory() = %#v, %v", revenues, err)
	}
	if revenues.DataAsOf == nil || *revenues.DataAsOf != "2026-06" {
		t.Fatalf("data_as_of 應來自伺服器 envelope,實際為 %#v", revenues.DataAsOf)
	}
	if *revenues.Revenues[0].YearOverYearPercent != 26.9 {
		t.Fatalf("年增率解析錯誤:%#v", revenues.Revenues[0])
	}
	if gotQuery != "from=2024-01&limit=24&to=2026-06" {
		t.Fatalf("query string 組裝錯誤:%q", gotQuery)
	}

	statements, err := client.FinancialStatementHistory(t.Context(), "2330", StatementHistoryOptions{PeriodType: "quarterly", Limit: 12})
	if err != nil || statements == nil || len(statements.Statements) != 1 || statements.Statements[0].Quarter != "Q1" {
		t.Fatalf("FinancialStatementHistory() = %#v, %v", statements, err)
	}
	if gotQuery != "limit=12&period_type=quarterly" {
		t.Fatalf("query string 組裝錯誤:%q", gotQuery)
	}

	dividends, err := client.DividendHistory(t.Context(), "2330", DividendHistoryOptions{FromYear: 2020, ToYear: 2026, Limit: 30})
	if err != nil || dividends == nil || len(dividends.Dividends) != 1 {
		t.Fatalf("DividendHistory() = %#v, %v", dividends, err)
	}
	if d := dividends.Dividends[0]; d.PaidYear != 2026 || d.DividendYear != 2025 || d.Quarter != "A" || d.ExDividendDate != nil {
		t.Fatalf("股利欄位解析錯誤:%#v", d)
	}
	if gotQuery != "from_year=2020&limit=30&to_year=2026" {
		t.Fatalf("query string 組裝錯誤:%q", gotQuery)
	}

	// 選填年度為 0(未提供)時不可出現在 query string。
	if _, err := client.DividendHistory(t.Context(), "2330", DividendHistoryOptions{Limit: 20}); err != nil {
		t.Fatalf("DividendHistory() 不應失敗:%v", err)
	}
	if gotQuery != "limit=20" {
		t.Fatalf("未提供的年度不應出現在 query string:%q", gotQuery)
	}

	// 已知股票但無資料:回傳空陣列(非 nil)且 data_as_of 為 nil。
	empty, err := client.MonthlyRevenueHistory(t.Context(), "4414", RevenueHistoryOptions{Limit: 24})
	if err != nil || empty == nil {
		t.Fatalf("空資料不應是錯誤:%#v, %v", empty, err)
	}
	if empty.Revenues == nil || len(empty.Revenues) != 0 || empty.DataAsOf != nil {
		t.Fatalf("空資料語意錯誤:%#v", empty)
	}

	// 未知股票:404 必須轉為 ErrStockNotFound,三個方法語意一致。
	if _, err := client.MonthlyRevenueHistory(t.Context(), "9999", RevenueHistoryOptions{Limit: 24}); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("MonthlyRevenueHistory 404 應為 ErrStockNotFound:%v", err)
	}
	if _, err := client.FinancialStatementHistory(t.Context(), "9999", StatementHistoryOptions{Limit: 12}); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("FinancialStatementHistory 404 應為 ErrStockNotFound:%v", err)
	}
	if _, err := client.DividendHistory(t.Context(), "9999", DividendHistoryOptions{Limit: 20}); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("DividendHistory 404 應為 ErrStockNotFound:%v", err)
	}
}

// TestAPIClientFinancialHistoryServerError 驗證 5xx 與無效 JSON 都回傳
// error,而不是被誤判成「股票不存在」或「查無資料」。
func TestAPIClientFinancialHistoryServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/500/monthly-revenues":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/stocks/BADJSON/dividends":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{not json`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	if _, err := client.MonthlyRevenueHistory(t.Context(), "500", RevenueHistoryOptions{Limit: 24}); err == nil || errors.Is(err, ErrStockNotFound) {
		t.Fatalf("5xx 不可視為股票不存在:%v", err)
	}
	if _, err := client.DividendHistory(t.Context(), "BADJSON", DividendHistoryOptions{Limit: 20}); err == nil || errors.Is(err, ErrStockNotFound) {
		t.Fatalf("無效 JSON 應回傳解析錯誤:%v", err)
	}
}
