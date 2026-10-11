// Package stock 測試 get_financial_health 的 API client、輸入驗證、摘要與註冊。
package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeHealthQuerier 是同時具備基本 Querier 與 FinancialHealthQuerier 的假資料來源。
type fakeHealthQuerier struct {
	fakeQuerier
	envelope *FinancialHealthEnvelope
	err      error
	gotYear  int
}

func (f *fakeHealthQuerier) FinancialHealth(_ context.Context, _ string, fiscalYear int) (*FinancialHealthEnvelope, error) {
	f.gotYear = fiscalYear
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

func healthItem(key, name, category, unit string, passed bool, value, baseline float64) HealthCriterion {
	return HealthCriterion{Key: key, Name: name, Category: category, Passed: ptr(passed), Value: ptr(value), Baseline: ptr(baseline), Unit: unit}
}

// sampleHealth 取 2330 2025 年對比 2024 年的實際數值(9/9)。
func sampleHealth() *FinancialHealthEnvelope {
	return &FinancialHealthEnvelope{
		StockSymbol: "2330", FiscalYear: ptr(2025), ComparedWith: ptr(2024), Score: 9, Evaluated: 9, MaxScore: 9,
		Criteria: []HealthCriterion{
			healthItem("roa_positive", "資產報酬率為正", "profitability", "ratio", true, 0.2162, 0),
			healthItem("operating_cash_flow_positive", "營業現金流為正", "profitability", "thousand_twd", true, 2_274_975_625, 0),
			healthItem("roa_improved", "資產報酬率較前一年提高", "profitability", "ratio", true, 0.2162, 0.1752),
			healthItem("cash_flow_exceeds_net_income", "營業現金流高於稅後淨利", "profitability", "thousand_twd", true, 2_274_975_625, 1_715_396_780),
			healthItem("leverage_not_increased", "長期負債比未較前一年上升", "leverage_liquidity", "ratio", true, 0.1278, 0.165),
			healthItem("current_ratio_improved", "流動比率較前一年提高", "leverage_liquidity", "ratio", true, 2.618, 2.4423),
			healthItem("no_new_shares", "股本未較前一年增加", "leverage_liquidity", "thousand_twd", true, 259_325_245, 259_327_332),
			healthItem("gross_margin_improved", "毛利率較前一年提高", "operating_efficiency", "ratio", true, 0.5989, 0.5612),
			healthItem("asset_turnover_improved", "資產周轉率較前一年提高", "operating_efficiency", "ratio", true, 0.4802, 0.4325),
		},
	}
}

// TestAPIClientFinancialHealth 驗證路徑、年度參數、404 語意與空陣列補齊。
func TestAPIClientFinancialHealth(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stocks/2330/financial-health":
			gotQuery = r.URL.RawQuery
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"stock_symbol":"2330","fiscal_year":null,"compared_with":null,"score":0,"evaluated":0,"max_score":9,"criteria":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	health, err := client.FinancialHealth(t.Context(), "2330", 2024)
	if err != nil || health.Criteria == nil || health.MaxScore != 9 {
		t.Fatalf("FinancialHealth() = %#v, %v", health, err)
	}
	if gotQuery != "fiscal_year=2024" {
		t.Fatalf("query string 錯誤:%q", gotQuery)
	}
	if _, err := client.FinancialHealth(t.Context(), "2330", 0); err != nil || gotQuery != "" {
		t.Fatalf("年度為 0 時不應帶參數:%q %v", gotQuery, err)
	}
	if _, err := client.FinancialHealth(t.Context(), "9999", 0); !errors.Is(err, ErrStockNotFound) {
		t.Fatalf("404 應回 ErrStockNotFound:%v", err)
	}
}

// TestFinancialHealthTool 驗證預設年度、範圍檢查、錯誤分層與摘要內容。
func TestFinancialHealthTool(t *testing.T) {
	newToolset := func(f *fakeHealthQuerier) *financialHealthToolset {
		return &financialHealthToolset{health: f, logf: func(string, ...any) {}}
	}

	f := &fakeHealthQuerier{envelope: sampleHealth()}
	result, output, err := newToolset(f).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: "2330"})
	if err != nil {
		t.Fatalf("financialHealth 不應失敗:%v", err)
	}
	if f.gotYear != 0 {
		t.Fatalf("未指定年度時應交給 API 決定,收到 %d", f.gotYear)
	}
	summary := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"股票 2330 財務體質(Piotroski F 分數,2025 年對比 2024 年):9/9 分,9 項皆可判定,屬體質強健區間(8–9 分)。",
		"\n獲利能力:\n- 資產報酬率為正:通過(21.62%)",
		"- 營業現金流為正:通過(22749.76 億元)",
		"- 營業現金流高於稅後淨利:通過(22749.76 億元 vs 17153.97 億元)",
		"\n財務槓桿與流動性:\n- 長期負債比未較前一年上升:通過(12.78% vs 16.50%)",
		"- 流動比率較前一年提高:通過(2.62 倍 vs 2.44 倍)",
		"- 股本未較前一年增加:通過(2593.25 億元 vs 2593.27 億元)",
		"\n經營效率:\n- 毛利率較前一年提高:通過(59.89% vs 56.12%)",
		"- 資產周轉率較前一年提高:通過(0.48 倍 vs 0.43 倍)",
		"免責聲明",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if out, ok := output.(FinancialHealthOutput); !ok || out.DataKind != "financial_health" || out.Score != 9 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}

	if _, _, err := newToolset(f).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: "2330", FiscalYear: 2024}); err != nil || f.gotYear != 2024 {
		t.Fatalf("指定年度應原樣傳給 API:%v %d", err, f.gotYear)
	}
	for _, year := range []int{1999, 2101} {
		if _, _, err := newToolset(f).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: "2330", FiscalYear: year}); err == nil || !strings.Contains(err.Error(), "fiscal_year") {
			t.Fatalf("年度 %d 應回錯誤:%v", year, err)
		}
	}
	if _, _, err := newToolset(f).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: " "}); err == nil {
		t.Fatal("空白代號應回錯誤")
	}
	if _, _, err := newToolset(&fakeHealthQuerier{err: ErrStockNotFound}).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: "9999"}); err == nil || !strings.Contains(err.Error(), "找不到股票代號") {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeHealthQuerier{err: errors.New("boom")}).financialHealth(t.Context(), nil, FinancialHealthInput{Symbol: "2330"}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
}

// TestFinancialHealthSummaryPartialAndEmpty 涵蓋部分指標無法判定、各分數區間與沒有年度財報。
func TestFinancialHealthSummaryPartialAndEmpty(t *testing.T) {
	e := sampleHealth()
	e.StockSymbol, e.Score, e.Evaluated = "2882", 4, 7
	e.Criteria[4].Passed, e.Criteria[4].Value, e.Criteria[4].Baseline = nil, nil, nil
	e.Criteria[5].Passed = nil
	e.Criteria[2].Passed = ptr(false)
	e.Criteria[8].Category = "unknown"
	summary := financialHealthSummary(e)
	for _, want := range []string{
		"4/9 分,僅 7 項可判定,其餘資料不足或不適用",
		"- 長期負債比未較前一年上升:無法判定\n",
		"- 流動比率較前一年提高:無法判定\n",
		"- 資產報酬率較前一年提高:未通過(21.62% vs 17.52%)",
		"\nunknown:\n- 資產周轉率較前一年提高",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if healthBand(2) != "屬體質偏弱區間(0–2 分)" || healthBand(5) != "屬中間區間(3–7 分)" {
		t.Fatal("分數區間文字錯誤")
	}
	if healthValue("roa_improved", "ratio", nil) != "無" || displayInt(nil) != "無" {
		t.Fatal("缺值應顯示「無」")
	}

	empty := financialHealthSummary(&FinancialHealthEnvelope{StockSymbol: "00679B", MaxScore: 9})
	if !strings.Contains(empty, "股票 00679B 尚無年度財報,無法計算財務體質評分。") || !strings.Contains(empty, "免責聲明") {
		t.Fatalf("摘要錯誤:%s", empty)
	}
}

// TestFinancialHealthToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀。
func TestFinancialHealthToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeHealthQuerier{}, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), t1, nil); err != nil {
		t.Fatalf("server.Connect:%v", err)
	}
	cs, err := client.Connect(t.Context(), t2, nil)
	if err != nil {
		t.Fatalf("client.Connect:%v", err)
	}
	defer cs.Close()
	found := false
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if tool.Name == "get_financial_health" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_financial_health 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_financial_health")
	}
}
