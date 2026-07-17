package stock

// 本檔案以同一組 HTTP fixture 驗證 Phase 1 Data API envelope 經過 client
// 解碼與 MCP tool handler 後仍完整保留，避免兩層各自測試成功、串接時卻
// 遺漏欄位或把 null、空陣列改成不同語意。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const (
	phase1MonthlyRevenueFixture = `{
		"stock_symbol":"2330","data_as_of":"2026-06","revenues":[{
			"month":"2026-06","monthly_revenue":2637099,"last_month_revenue":null,
			"last_year_same_month_revenue":2078694,"monthly_accumulated_revenue":15840001,
			"last_year_monthly_accumulated_revenue":13300000,"month_over_month_percent":0,
			"year_over_year_percent":26.86,"accumulated_year_over_year_percent":19.1,
			"average_price":1045.5,"lowest_price":null,"highest_price":1100
		}]
	}`
	phase1StatementFixture = `{
		"stock_symbol":"2330","data_as_of":"2026-Q1","statements":[{
			"year":2026,"quarter":"Q1","gross_profit_margin":58.79,
			"operating_profit_margin":48.5,"pre_tax_income_margin":null,
			"net_income_margin":42.01,"net_asset_value_per_share":170.25,
			"sales_per_share":23.1,"earnings_per_share":13.94,
			"profit_before_tax_per_share":15.2,"return_on_equity":8.9,
			"return_on_assets":0,"updated_at":"2026-05-15T08:30:00Z"
		}]
	}`
	phase1DividendFixture = `{
		"stock_symbol":"2330","data_as_of":"2025-A","dividends":[{
			"paid_year":2026,"dividend_year":2025,"quarter":"A",
			"cash_dividend":16,"stock_dividend":0,"total_dividend":16,
			"earnings_cash_dividend":15,"capital_reserve_cash_dividend":1,
			"earnings_stock_dividend":null,"capital_reserve_stock_dividend":0,
			"cash_payout_ratio":62.5,"stock_payout_ratio":null,"total_payout_ratio":62.5,
			"ex_dividend_date":"2026-06-18","ex_rights_date":null,
			"cash_payable_date":"2026-07-16","stock_payable_date":null,
			"updated_at":"2026-05-20T01:02:03Z"
		}]
	}`
)

// TestPhase1APIEnvelopeToMCPContract 驗證三個 Phase 1 endpoint 的所有欄位
// 從 HTTP 回應一路進入 structuredContent，包含 0、null 與 [] 的差異。
func TestPhase1APIEnvelopeToMCPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/stocks/2330/monthly-revenues":
			_, _ = w.Write([]byte(phase1MonthlyRevenueFixture))
		case "/api/v1/stocks/2330/financial-statements":
			_, _ = w.Write([]byte(phase1StatementFixture))
		case "/api/v1/stocks/2330/dividends":
			_, _ = w.Write([]byte(phase1DividendFixture))
		case "/api/v1/stocks/EMPTY/monthly-revenues":
			_, _ = w.Write([]byte(`{"stock_symbol":"EMPTY","data_as_of":null,"revenues":[]}`))
		case "/api/v1/stocks/EMPTY/financial-statements":
			_, _ = w.Write([]byte(`{"stock_symbol":"EMPTY","data_as_of":null,"statements":[]}`))
		case "/api/v1/stocks/EMPTY/dividends":
			_, _ = w.Write([]byte(`{"stock_symbol":"EMPTY","data_as_of":null,"dividends":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "contract-test", time.Second)

	t.Run("月營收完整欄位", func(t *testing.T) {
		history, err := client.MonthlyRevenueHistory(t.Context(), "2330", RevenueHistoryOptions{Limit: 24})
		if err != nil {
			t.Fatalf("API client 解碼失敗:%v", err)
		}
		_, out, err := newFinancialToolset(&fakeFinancialQuerier{revenues: history}).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("tool handler 執行失敗:%v", err)
		}
		assertPhase1EnvelopePreserved(t, history, out)
	})

	t.Run("財報完整欄位", func(t *testing.T) {
		history, err := client.FinancialStatementHistory(t.Context(), "2330", StatementHistoryOptions{PeriodType: "all", Limit: 12})
		if err != nil {
			t.Fatalf("API client 解碼失敗:%v", err)
		}
		_, out, err := newFinancialToolset(&fakeFinancialQuerier{statements: history}).financialStatementHistory(t.Context(), nil, StatementHistoryInput{Symbol: "2330", PeriodType: "all"})
		if err != nil {
			t.Fatalf("tool handler 執行失敗:%v", err)
		}
		assertPhase1EnvelopePreserved(t, history, out)
	})

	t.Run("股利完整欄位", func(t *testing.T) {
		history, err := client.DividendHistory(t.Context(), "2330", DividendHistoryOptions{Limit: 20})
		if err != nil {
			t.Fatalf("API client 解碼失敗:%v", err)
		}
		_, out, err := newFinancialToolset(&fakeFinancialQuerier{dividends: history}).dividendHistory(t.Context(), nil, DividendHistoryInput{Symbol: "2330"})
		if err != nil {
			t.Fatalf("tool handler 執行失敗:%v", err)
		}
		assertPhase1EnvelopePreserved(t, history, out)
	})

	// 空資料同樣走 client 與 handler，確保 data_as_of:null 與資料清單 []
	// 不會在任何一層被省略、改成空字串或序列化成 null。
	for _, tc := range []struct {
		name string
		call func(*testing.T) (any, any)
	}{
		{"月營收空陣列", func(t *testing.T) (any, any) {
			history, err := client.MonthlyRevenueHistory(t.Context(), "EMPTY", RevenueHistoryOptions{Limit: 24})
			if err != nil {
				t.Fatalf("API client 解碼失敗:%v", err)
			}
			_, out, err := newFinancialToolset(&fakeFinancialQuerier{revenues: history}).monthlyRevenueHistory(t.Context(), nil, MonthlyRevenueInput{Symbol: "EMPTY"})
			if err != nil {
				t.Fatalf("tool handler 執行失敗:%v", err)
			}
			return history, out
		}},
		{"財報空陣列", func(t *testing.T) (any, any) {
			history, err := client.FinancialStatementHistory(t.Context(), "EMPTY", StatementHistoryOptions{PeriodType: "quarterly", Limit: 12})
			if err != nil {
				t.Fatalf("API client 解碼失敗:%v", err)
			}
			_, out, err := newFinancialToolset(&fakeFinancialQuerier{statements: history}).financialStatementHistory(t.Context(), nil, StatementHistoryInput{Symbol: "EMPTY"})
			if err != nil {
				t.Fatalf("tool handler 執行失敗:%v", err)
			}
			return history, out
		}},
		{"股利空陣列", func(t *testing.T) (any, any) {
			history, err := client.DividendHistory(t.Context(), "EMPTY", DividendHistoryOptions{Limit: 20})
			if err != nil {
				t.Fatalf("API client 解碼失敗:%v", err)
			}
			_, out, err := newFinancialToolset(&fakeFinancialQuerier{dividends: history}).dividendHistory(t.Context(), nil, DividendHistoryInput{Symbol: "EMPTY"})
			if err != nil {
				t.Fatalf("tool handler 執行失敗:%v", err)
			}
			return history, out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, out := tc.call(t)
			assertPhase1EnvelopePreserved(t, envelope, out)
		})
	}
}

// assertPhase1EnvelopePreserved 移除 MCP 額外加入的共通 metadata 後，以
// JSON 語意逐欄比對 Data API envelope；這會同時檢查欄位名稱、值、null
// 與陣列形狀，而不受 Go struct 型別差異影響。
func assertPhase1EnvelopePreserved(t *testing.T, envelope, structuredContent any) {
	t.Helper()
	want := phase1JSONMap(t, envelope)
	got := phase1JSONMap(t, structuredContent)
	delete(got, "data_kind")
	delete(got, "is_realtime")
	delete(got, "disclaimer")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("structuredContent 未原樣保留 API envelope\nwant:%#v\n got:%#v", want, got)
	}
}

// phase1JSONMap 把任意 envelope/output 轉成一般 JSON 物件，供契約比對。
func phase1JSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("JSON 編碼失敗:%v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("JSON 解碼失敗:%v", err)
	}
	return result
}
