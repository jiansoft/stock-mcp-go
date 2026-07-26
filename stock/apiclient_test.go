// Package stock 測試 APIClient 對 stock_rust HTTP 契約的狀態碼、認證與 JSON 轉換行為。
package stock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestAPIClientFinancialHistoryStatusMatrix 逐一驗證三個 Phase 1 endpoint 的
// 401、422 與 timeout。這些都不是「股票不存在」，因此不得轉成
// ErrStockNotFound；tool 層會把詳細 error 留在 log 並回安全通用訊息。
func TestAPIClientFinancialHistoryStatusMatrix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/401/"):
			w.WriteHeader(http.StatusUnauthorized)
		case strings.Contains(r.URL.Path, "/422/"):
			w.WriteHeader(http.StatusUnprocessableEntity)
		case strings.Contains(r.URL.Path, "/slow/"):
			// 等 client timeout 取消 request context，不用 time.Sleep 猜測
			// goroutine 排程，因此測試快速且 deterministic。
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	type endpoint struct {
		name string
		call func(*APIClient, string) error
	}
	endpoints := []endpoint{
		{"monthly-revenues", func(c *APIClient, symbol string) error {
			_, err := c.MonthlyRevenueHistory(t.Context(), symbol, RevenueHistoryOptions{Limit: 24})
			return err
		}},
		{"financial-statements", func(c *APIClient, symbol string) error {
			_, err := c.FinancialStatementHistory(t.Context(), symbol, StatementHistoryOptions{PeriodType: "quarterly", Limit: 12})
			return err
		}},
		{"dividends", func(c *APIClient, symbol string) error {
			_, err := c.DividendHistory(t.Context(), symbol, DividendHistoryOptions{Limit: 20})
			return err
		}},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, status := range []string{"401", "422"} {
				t.Run(status, func(t *testing.T) {
					err := endpoint.call(NewAPIClient(server.URL, "secret", time.Second), status)
					if err == nil || errors.Is(err, ErrStockNotFound) {
						t.Fatalf("%s 不可轉成 ErrStockNotFound:%v", status, err)
					}
				})
			}
			t.Run("timeout", func(t *testing.T) {
				err := endpoint.call(NewAPIClient(server.URL, "secret", 10*time.Millisecond), "slow")
				if err == nil || errors.Is(err, ErrStockNotFound) {
					t.Fatalf("timeout 必須是一般 error:%v", err)
				}
			})
		})
	}
}

// TestAPIClientAnalytics 驗證 Phase 2 三個 endpoint 的完整 envelope、query
// 組裝、空陣列與 null 語意。測試 fixture 也會在 tool 測試重用相同數值，
// 固定 Data API 到 MCP structuredContent 之間不做重新計算。
func TestAPIClientAnalytics(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/stocks/2330/valuation":
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","data_as_of":"2026-07-16","valuation":{"stock_symbol":"2330","date":"2026-07-16","closing_price":1045,"percentage":50,"year_count":5,"cheap":800,"fair":1000,"expensive":1200,"valuation_band":"overvalued"}}`))
		case "/api/v1/stocks/4414/valuation":
			_, _ = w.Write([]byte(`{"stock_symbol":"4414","data_as_of":null,"valuation":null}`))
		case "/api/v1/market/breadth":
			_, _ = w.Write([]byte(`{"data_as_of":"2026-07-16","breadth":{"date":"2026-07-16","market":"all","undervalued":100,"fair_valued":200,"overvalued":300,"highly_overvalued":50,"stocks_up":700,"stocks_down":400,"stocks_unchanged":100,"updated_at":null},"history":[{"date":"2026-07-16","market":"all","undervalued":100,"fair_valued":200,"overvalued":300,"highly_overvalued":50,"stocks_up":700,"stocks_down":400,"stocks_unchanged":100,"updated_at":null}]}`))
		case "/api/v1/market/dividend-yield-ranking":
			_, _ = w.Write([]byte(`{"data_as_of":"2026-07-16","stocks":[{"rank":1,"stock_symbol":"1234","name":"測試公司","market_id":2,"industry_id":24,"date":"2026-07-16","closing_price":50,"dividend":5,"dividend_yield_percent":10}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	valuation, err := client.StockValuation(t.Context(), "2330", ValuationOptions{Date: "2026-07-16"})
	if err != nil || valuation.Valuation == nil || valuation.DataAsOf == nil || *valuation.DataAsOf != "2026-07-16" {
		t.Fatalf("StockValuation() = %#v, %v", valuation, err)
	}
	if valuation.Valuation.ValuationBand != "overvalued" || *valuation.Valuation.ClosingPrice != 1045 {
		t.Fatalf("估值欄位解析錯誤:%#v", valuation.Valuation)
	}
	if gotQuery != "date=2026-07-16" {
		t.Fatalf("valuation query string 錯誤:%q", gotQuery)
	}

	empty, err := client.StockValuation(t.Context(), "4414", ValuationOptions{})
	if err != nil || empty.Valuation != nil || empty.DataAsOf != nil {
		t.Fatalf("估值空資料應保留 null:%#v, %v", empty, err)
	}

	breadth, err := client.MarketBreadth(t.Context(), MarketBreadthOptions{Market: "all", Date: "2026-07-16", Days: 20})
	if err != nil || len(breadth.History) != 1 {
		t.Fatalf("MarketBreadth() = %#v, %v", breadth, err)
	}
	if breadth.Breadth.StocksUp != 700 || breadth.History[0].StocksUp != breadth.Breadth.StocksUp {
		t.Fatalf("breadth 必須等於 history[0]:%#v", breadth)
	}
	if gotQuery != "date=2026-07-16&days=20&market=all" {
		t.Fatalf("breadth query string 錯誤:%q", gotQuery)
	}

	ranking, err := client.DividendYieldRanking(t.Context(), YieldRankingOptions{Date: "2026-07-16", Market: "twse", IndustryID: 24, Limit: 20})
	if err != nil || len(ranking.Stocks) != 1 || ranking.Stocks[0].DividendYieldPercent == nil || *ranking.Stocks[0].DividendYieldPercent != 10 {
		t.Fatalf("DividendYieldRanking() = %#v, %v", ranking, err)
	}
	if gotQuery != "date=2026-07-16&industry_id=24&limit=20&market=twse" {
		t.Fatalf("ranking query string 錯誤:%q", gotQuery)
	}
}

// TestAPIClientAnalyticsErrors 以 table-driven 測試固定 404、401、422、
// 500、timeout 與無效 JSON 的分層語意。只有個股估值 404 會轉成
// ErrStockNotFound；其餘錯誤都必須維持一般 error，交由 tool 隱藏細節。
func TestAPIClientAnalyticsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/404/valuation":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/stocks/401/valuation":
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/v1/market/breadth":
			switch r.URL.Query().Get("market") {
			case "slow":
				// client timeout 取消 request context 後 handler 立即退出，不用
				// time.Sleep 猜測排程時間，因此測試快速且不會偶發失敗。
				<-r.Context().Done()
				return
			case "notfound":
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "/api/v1/market/dividend-yield-ranking":
			switch r.URL.Query().Get("market") {
			case "badjson":
				_, _ = w.Write([]byte(`{not-json`))
			case "notfound":
				w.WriteHeader(http.StatusNotFound)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "secret", time.Second)
	tests := []struct {
		name         string
		call         func(*APIClient) error
		wantNotFound bool
	}{
		{"valuation 404", func(c *APIClient) error {
			_, err := c.StockValuation(t.Context(), "404", ValuationOptions{})
			return err
		}, true},
		{"valuation 401", func(c *APIClient) error {
			_, err := c.StockValuation(t.Context(), "401", ValuationOptions{})
			return err
		}, false},
		{"breadth 422", func(c *APIClient) error {
			_, err := c.MarketBreadth(t.Context(), MarketBreadthOptions{Market: "all", Days: 61})
			return err
		}, false},
		{"ranking 500", func(c *APIClient) error {
			_, err := c.DividendYieldRanking(t.Context(), YieldRankingOptions{Market: "all", Limit: 20})
			return err
		}, false},
		{"ranking invalid JSON", func(c *APIClient) error {
			_, err := c.DividendYieldRanking(t.Context(), YieldRankingOptions{Market: "badjson", Limit: 20})
			return err
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(client)
			if err == nil {
				t.Fatal("預期錯誤,實際成功")
			}
			if errors.Is(err, ErrStockNotFound) != tc.wantNotFound {
				t.Fatalf("ErrStockNotFound 語意錯誤:%v", err)
			}
		})
	}

	shortClient := NewAPIClient(server.URL, "secret", 10*time.Millisecond)
	if _, err := shortClient.MarketBreadth(t.Context(), MarketBreadthOptions{Market: "slow", Days: 1}); err == nil {
		t.Fatal("timeout 必須回傳 error")
	}
	for name, call := range map[string]func() error{
		"breadth": func() error {
			_, err := client.MarketBreadth(t.Context(), MarketBreadthOptions{Market: "notfound", Days: 1})
			return err
		},
		"ranking": func() error {
			_, err := client.DividendYieldRanking(t.Context(), YieldRankingOptions{Market: "notfound", Limit: 20})
			return err
		},
	} {
		t.Run(name+" 404", func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrMarketDataNotFound) {
				t.Fatalf("市場資料 404 應轉為 ErrMarketDataNotFound:%v", err)
			}
		})
	}
}

// TestAPIClientScreenStocks 驗證 Phase 3 endpoint 的完整 envelope、固定 query
// 白名單、零值門檻與 null/空陣列語意。
func TestAPIClientScreenStocks(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		switch r.URL.Query().Get("industry_id") {
		case "24":
			_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[{"stock_symbol":"2330","name":"台積電","market_id":2,"industry_id":24,"revenue_yoy_percent":26.9,"earnings_per_share":13.94,"return_on_equity":8.9,"dividend_yield_percent":2.1,"valuation_band":"fair_valued","valuation_percentage":130.6,"revenue_month":"2026-06","financial_period":"2026-Q1","valuation_date":"2026-07-16","yield_date":null}]}`))
		default:
			// 刻意回 stocks:null，驗證 client 對外防禦性轉成 []。
			_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":null}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	zero := 0.0
	result, err := client.ScreenStocks(t.Context(), ScreenOptions{
		Market: "twse", IndustryID: 24, ValuationBand: "fair_valued",
		MinRevenueYOYPercent: &zero, MinEPS: ptr(5.0), MinROEPercent: ptr(8.0), MinDividendYieldPercent: ptr(2.0),
		SortBy: "dividend_yield", SortOrder: "desc", Limit: 20,
	})
	if err != nil || result == nil || len(result.Stocks) != 1 {
		t.Fatalf("ScreenStocks() = %#v, %v", result, err)
	}
	stock := result.Stocks[0]
	if result.DataAsOf != nil || stock.StockSymbol != "2330" || stock.YieldDate != nil || *stock.RevenueYOYPercent != 26.9 {
		t.Fatalf("screen envelope/null 欄位解析錯誤:%#v", result)
	}
	wantQuery := "industry_id=24&limit=20&market=twse&min_dividend_yield_percent=2&min_eps=5&min_revenue_yoy_percent=0&min_roe_percent=8&sort_by=dividend_yield&sort_order=desc&valuation_band=fair_valued"
	if gotQuery != wantQuery {
		t.Fatalf("screen query string 錯誤:\n got %q\nwant %q", gotQuery, wantQuery)
	}

	empty, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "tpex", SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
	if err != nil || empty.Stocks == nil || len(empty.Stocks) != 0 || empty.DataAsOf != nil {
		t.Fatalf("空結果必須保留 []/null:%#v, %v", empty, err)
	}
}

// TestAPIClientScreenStocksErrors 覆蓋 screen endpoint 的 404、401、422、
// 500、timeout 與無效 JSON。選股沒有「個股不存在」語意，因此所有
// 非 200 狀態都是一般 error，不能轉成 ErrStockNotFound。
func TestAPIClientScreenStocksErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("market") {
		case "notfound":
			w.WriteHeader(http.StatusNotFound)
		case "unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
		case "invalid":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "badjson":
			_, _ = w.Write([]byte(`{not-json`))
		case "slow":
			<-r.Context().Done()
		default:
			_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[]}`))
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "secret", time.Second)
	for _, market := range []string{"notfound", "unauthorized", "invalid", "broken", "badjson"} {
		t.Run(market, func(t *testing.T) {
			_, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: market, SortBy: "stock_symbol", SortOrder: "asc", Limit: 20})
			if err == nil || errors.Is(err, ErrStockNotFound) {
				t.Fatalf("%s 必須是一般 error:%v", market, err)
			}
		})
	}
	shortClient := NewAPIClient(server.URL, "secret", 10*time.Millisecond)
	if _, err := shortClient.ScreenStocks(t.Context(), ScreenOptions{Market: "slow", SortBy: "stock_symbol", SortOrder: "asc", Limit: 20}); err == nil {
		t.Fatal("timeout 必須回傳 error")
	}
}

// TestAPIClientMarketData 驗證 Phase 4 三個市場輔助 endpoint 的完整
// envelope、query string 組裝(選填參數未提供時不出現)、null 欄位與
// 空陣列語意。fixture 數值會在 tool 測試重用,固定 Data API 到 MCP
// structuredContent 之間不做重新計算。
func TestAPIClientMarketData(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/market/index-history":
			if r.URL.Query().Get("from") == "2030-01-01" {
				// 合法條件但查無資料:200 + 空陣列 + data_as_of null。
				_, _ = w.Write([]byte(`{"data_as_of":null,"points":[]}`))
				return
			}
			// trade_value/transaction/trading_volume 帶 null,驗證數值欄位
			// 的缺值語意被原樣保留。
			_, _ = w.Write([]byte(`{"data_as_of":"2026-07-17","points":[{"date":"2026-07-17","index":23000.5,"change":120.3,"trade_value":412345678901,"transaction":null,"trading_volume":null},{"date":"2026-07-16","index":22880.2,"change":-50.1,"trade_value":null,"transaction":1234567,"trading_volume":9876543210}]}`))
		case "/api/v1/market/dividend-calendar":
			if r.URL.Query().Get("event_type") == "ex_rights" {
				_, _ = w.Write([]byte(`{"data_as_of":null,"events":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data_as_of":null,"events":[{"stock_symbol":"2330","name":"台積電","event_type":"ex_dividend","event_date":"2026-07-20","dividend_year":2025,"quarter":"A","cash_dividend":17,"stock_dividend":0,"total_dividend":17}]}`))
		case "/api/v1/market/qfii-holding-ranking":
			if r.URL.Query().Get("industry_id") == "99" {
				// 刻意回 stocks:null,驗證 client 對外防禦性轉成 []。
				_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[{"rank":1,"stock_symbol":"2330","name":"台積電","market_id":2,"industry_id":24,"qfii_shares_held":19000000000,"qfii_share_holding_percentage":73.2,"issued_share":25930000000}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	// --- 指數歷史:完整欄位與 null 語意 ---
	index, err := client.MarketIndexHistory(t.Context(), IndexHistoryOptions{From: "2026-06-01", To: "2026-07-17", Limit: 30})
	if err != nil || index == nil || len(index.Points) != 2 {
		t.Fatalf("MarketIndexHistory() = %#v, %v", index, err)
	}
	if index.DataAsOf == nil || *index.DataAsOf != "2026-07-17" {
		t.Fatalf("data_as_of 應來自伺服器 envelope:%#v", index.DataAsOf)
	}
	latest := index.Points[0]
	if *latest.Index != 23000.5 || *latest.Change != 120.3 || latest.Transaction != nil || latest.TradingVolume != nil {
		t.Fatalf("指數欄位/null 語意解析錯誤:%#v", latest)
	}
	if gotQuery != "from=2026-06-01&limit=30&to=2026-07-17" {
		t.Fatalf("index-history query string 錯誤:%q", gotQuery)
	}

	// 選填 from/to 未提供時不可出現在 query string。
	if _, err := client.MarketIndexHistory(t.Context(), IndexHistoryOptions{Limit: 30}); err != nil {
		t.Fatalf("MarketIndexHistory() 不應失敗:%v", err)
	}
	if gotQuery != "limit=30" {
		t.Fatalf("未提供的日期不應出現在 query string:%q", gotQuery)
	}

	// 合法條件查無資料:200 空陣列不是錯誤,data_as_of 為 nil。
	emptyIndex, err := client.MarketIndexHistory(t.Context(), IndexHistoryOptions{From: "2030-01-01", Limit: 30})
	if err != nil || emptyIndex.Points == nil || len(emptyIndex.Points) != 0 || emptyIndex.DataAsOf != nil {
		t.Fatalf("指數空資料語意錯誤:%#v, %v", emptyIndex, err)
	}

	// --- 股利行事曆:完整欄位與 data_as_of null ---
	calendar, err := client.DividendCalendar(t.Context(), CalendarOptions{From: "2026-07-01", To: "2026-07-31", EventType: "all", Limit: 50})
	if err != nil || calendar == nil || len(calendar.Events) != 1 {
		t.Fatalf("DividendCalendar() = %#v, %v", calendar, err)
	}
	if calendar.DataAsOf != nil {
		t.Fatalf("行事曆 data_as_of 依契約固定為 null:%#v", calendar.DataAsOf)
	}
	event := calendar.Events[0]
	if event.StockSymbol != "2330" || event.EventType != "ex_dividend" || event.EventDate != "2026-07-20" ||
		event.DividendYear != 2025 || event.Quarter != "A" || *event.CashDividend != 17 || *event.StockDividend != 0 {
		t.Fatalf("行事曆事件欄位解析錯誤:%#v", event)
	}
	if gotQuery != "event_type=all&from=2026-07-01&limit=50&to=2026-07-31" {
		t.Fatalf("dividend-calendar query string 錯誤:%q", gotQuery)
	}

	// 選填 from/to 未提供時不出現;event_type 由 tool 層保證有值,固定送出。
	emptyCalendar, err := client.DividendCalendar(t.Context(), CalendarOptions{EventType: "ex_rights", Limit: 50})
	if err != nil || emptyCalendar.Events == nil || len(emptyCalendar.Events) != 0 {
		t.Fatalf("行事曆空資料語意錯誤:%#v, %v", emptyCalendar, err)
	}
	if gotQuery != "event_type=ex_rights&limit=50" {
		t.Fatalf("未提供的日期不應出現在 query string:%q", gotQuery)
	}

	// --- QFII 排行:完整欄位、int64 股數與 data_as_of null ---
	ranking, err := client.QfiiHoldingRanking(t.Context(), QfiiRankingOptions{Market: "twse", IndustryID: 24, SortBy: "percentage", Limit: 20})
	if err != nil || ranking == nil || len(ranking.Stocks) != 1 {
		t.Fatalf("QfiiHoldingRanking() = %#v, %v", ranking, err)
	}
	if ranking.DataAsOf != nil {
		t.Fatalf("QFII 排行 data_as_of 依契約固定為 null:%#v", ranking.DataAsOf)
	}
	top := ranking.Stocks[0]
	if top.Rank != 1 || top.StockSymbol != "2330" || *top.QfiiSharesHeld != 19000000000 ||
		*top.QfiiShareHoldingPercentage != 73.2 || *top.IssuedShare != 25930000000 {
		t.Fatalf("QFII 欄位解析錯誤:%#v", top)
	}
	if gotQuery != "industry_id=24&limit=20&market=twse&sort_by=percentage" {
		t.Fatalf("qfii-holding-ranking query string 錯誤:%q", gotQuery)
	}

	// 選填 industry_id 為 0(未提供)時不出現;stocks:null 防禦性轉成 []。
	emptyRanking, err := client.QfiiHoldingRanking(t.Context(), QfiiRankingOptions{Market: "all", IndustryID: 99, SortBy: "shares", Limit: 20})
	if err != nil || emptyRanking.Stocks == nil || len(emptyRanking.Stocks) != 0 {
		t.Fatalf("QFII 空資料語意錯誤:%#v, %v", emptyRanking, err)
	}
	if _, err := client.QfiiHoldingRanking(t.Context(), QfiiRankingOptions{Market: "all", SortBy: "percentage", Limit: 20}); err != nil {
		t.Fatalf("QfiiHoldingRanking() 不應失敗:%v", err)
	}
	if gotQuery != "limit=20&market=all&sort_by=percentage" {
		t.Fatalf("未提供的 industry_id 不應出現在 query string:%q", gotQuery)
	}
}

// TestAPIClientMarketDataErrors 覆蓋 Phase 4 三個 endpoint 的 404、401、
// 422、500、timeout 與無效 JSON。三個都是市場層級 endpoint,契約上沒有
// 404 語意——404 代表部署異常,必須與其他非 2xx 一樣是一般 error,
// 不得轉成 ErrStockNotFound 或 ErrMarketDataNotFound。
func TestAPIClientMarketDataErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 以 limit 值挑選要模擬的故障情境,讓同一個 handler 服務三個 path。
		switch r.URL.Query().Get("limit") {
		case "1":
			w.WriteHeader(http.StatusNotFound)
		case "2":
			w.WriteHeader(http.StatusUnauthorized)
		case "3":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "4":
			w.WriteHeader(http.StatusInternalServerError)
		case "5":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{not-json`))
		case "6":
			// 等 client timeout 取消 request context,不用 time.Sleep 猜測
			// goroutine 排程,因此測試快速且 deterministic。
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	endpoints := map[string]func(*APIClient, int) error{
		"index-history": func(c *APIClient, limit int) error {
			_, err := c.MarketIndexHistory(t.Context(), IndexHistoryOptions{Limit: limit})
			return err
		},
		"dividend-calendar": func(c *APIClient, limit int) error {
			_, err := c.DividendCalendar(t.Context(), CalendarOptions{EventType: "all", Limit: limit})
			return err
		},
		"qfii-holding-ranking": func(c *APIClient, limit int) error {
			_, err := c.QfiiHoldingRanking(t.Context(), QfiiRankingOptions{Market: "all", SortBy: "percentage", Limit: limit})
			return err
		},
	}
	scenarios := []struct {
		name  string
		limit int
	}{
		{"404", 1}, {"401", 2}, {"422", 3}, {"500", 4}, {"invalid JSON", 5},
	}
	for name, call := range endpoints {
		t.Run(name, func(t *testing.T) {
			client := NewAPIClient(server.URL, "secret", time.Second)
			for _, sc := range scenarios {
				t.Run(sc.name, func(t *testing.T) {
					err := call(client, sc.limit)
					if err == nil {
						t.Fatal("預期錯誤,實際成功")
					}
					if errors.Is(err, ErrStockNotFound) || errors.Is(err, ErrMarketDataNotFound) {
						t.Fatalf("市場輔助 endpoint 的錯誤不得帶個股/市場 404 語意:%v", err)
					}
				})
			}
			t.Run("timeout", func(t *testing.T) {
				shortClient := NewAPIClient(server.URL, "secret", 10*time.Millisecond)
				if err := call(shortClient, 6); err == nil {
					t.Fatal("timeout 必須回傳 error")
				}
			})
		})
	}
}

// TestAPIClientErrorPathsDrainBody 驗證「關閉前先 drain response body」
// 這個修改沒有改變任何既有語意:404 仍然是「查無資料」、非 2xx 仍然是
// 一般錯誤,而且能連續正確處理多次。
//
// ## 為什麼這裡不斷言「連線被重用了幾條」
//
// 加上 drain 的原意是讓底層 TCP 連線能回到連線池重用(Go 的
// http.Transport 只在 response body 被完整讀到 EOF 之後才保留連線)。
// 但實測顯示 Go 的 transport 本身已經會自行處理掉大部分情況:對這個
// 測試的 20 次錯誤請求,有 drain 是建立 1 條連線、沒有 drain 是 2 條——
// 差異真實存在但非常小,遠不到值得用一個依賴 transport 內部行為的
// 脆弱斷言去釘死的程度(那種測試會在 Go 版本更新時無預警壞掉)。
//
// 因此這裡只測「語意不變」這件確定且穩定的事;drain 本身保留,因為它
// 在語意上是正確的做法(用完的 body 就該讀完再關),成本也趨近於零。
func TestAPIClientErrorPathsDrainBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/profile") {
			// 附帶內容的 404,模擬真實 API 會回錯誤說明的情況。
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"stock not found","detail":"no such symbol"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "secret", 5*time.Second)

	// 連續多次,確認每一次的行為都一致——如果 drain 寫錯(例如不小心把
	// 成功路徑的 body 也提前吃掉),重複執行最容易把問題暴露出來。
	for i := range 5 {
		profile, err := client.StockProfile(t.Context(), "2330")
		if err != nil || profile != nil {
			t.Fatalf("第 %d 次 StockProfile 404 應回傳 (nil, nil),實際為 %#v, %v", i+1, profile, err)
		}
		if _, err := client.MarketIndexHistory(t.Context(), IndexHistoryOptions{Limit: 10}); err == nil {
			t.Fatalf("第 %d 次 5xx 應回傳錯誤", i+1)
		}
	}
}
