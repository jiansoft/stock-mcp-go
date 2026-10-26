package stock

// 本檔案測試回測情境比較工具:情境驗證與編碼、查詢參數、摘要文字與錯誤處理。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeCompareQuerier struct {
	envelope  *ScreenBacktestCompareEnvelope
	err       error
	got       ScreenBacktestOptions
	scenarios []url.Values
}

func (f *fakeCompareQuerier) ScreenBacktestCompare(_ context.Context, opt ScreenBacktestOptions, scenarios []url.Values) (*ScreenBacktestCompareEnvelope, error) {
	f.got, f.scenarios = opt, scenarios
	return f.envelope, f.err
}

func sampleCompare() *ScreenBacktestCompareEnvelope {
	daily := -25.0
	return &ScreenBacktestCompareEnvelope{Scenarios: []BacktestScenarioResult{
		{Label: "收盤", ExecutionModel: "close", Summary: BacktestSummary{Periods: 40, AnnualizedAfterCosts: ptr(30.0), SharpeRatio: ptr(1.5), DailyMaxDrawdownAfterCosts: &daily}},
		{Label: "次日開盤", ExecutionModel: "next_open", SlippageBps: 20, Summary: BacktestSummary{Periods: 40, AnnualizedAfterCosts: ptr(28.5), SharpeRatio: ptr(1.4), MaxDrawdownAfterCosts: ptr(-20.0)}},
	}}
}

func TestCompareToolValidatesAndEncodesScenarios(t *testing.T) {
	f := &fakeCompareQuerier{envelope: sampleCompare()}
	ts := &screenBacktestCompareToolset{cq: f, logf: func(string, ...any) {}}
	in := ScreenBacktestCompareInput{
		ScreenBacktestInput: ScreenBacktestInput{HoldDays: 20, StopLoss: 10},
		Scenarios: []BacktestScenarioInput{
			{Label: " 收盤 ", SlippageBps: ptr(0)},
			{Label: "低費率", FeeDiscount: ptr(0.28), EtfWeight: ptr(50), MaxWeight: ptr(10), KeepRank: ptr(30), RebalanceOffset: ptr(3)},
		},
	}
	result, output, err := ts.screenBacktestCompare(t.Context(), nil, in)
	if err != nil {
		t.Fatalf("合法情境不應失敗:%v", err)
	}
	if f.scenarios[0].Get("label") != "收盤" || f.scenarios[0].Get("slippage_bps") != "0" {
		t.Fatalf("情境 1 編碼錯誤:%v", f.scenarios[0])
	}
	if f.scenarios[1].Get("fee_discount") != "0.28" || f.scenarios[1].Get("rebalance_offset") != "3" || f.scenarios[1].Get("keep_rank") != "30" {
		t.Fatalf("情境 2 編碼錯誤:%v", f.scenarios[1])
	}
	if out, ok := output.(ScreenBacktestCompareOutput); !ok || out.DataKind != "screen_backtest_compare" {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "共 2 個情境") {
		t.Fatalf("摘要錯誤:%s", text)
	}
	for _, bad := range []ScreenBacktestCompareInput{
		{Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}}},
		{Scenarios: make([]BacktestScenarioInput, 5)},
		{Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {Label: "沒改東西"}}},
		{Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {SlippageBps: ptr(201)}}},
		{Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {ExecutionPrice: "vwap"}}},
		{ScreenBacktestInput: ScreenBacktestInput{StopLoss: 10}, Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {ExecutionPrice: "next_open"}}},
		{Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {SlippageBps: ptr(2), Label: strings.Repeat("長", 41)}}},
		{ScreenBacktestInput: ScreenBacktestInput{HoldDays: 7}, Scenarios: []BacktestScenarioInput{{SlippageBps: ptr(1)}, {SlippageBps: ptr(2)}}},
	} {
		if _, _, err := ts.screenBacktestCompare(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	ts.cq = &fakeCompareQuerier{err: errors.New("boom")}
	if _, _, err := ts.screenBacktestCompare(t.Context(), nil, in); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩:%v", err)
	}
}

func TestCompareSummaryShowsScenariosAndDeltas(t *testing.T) {
	text := screenBacktestCompareSummary(sampleCompare(), ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	for _, want := range []string{"共 2 個情境", "收盤(換股日收盤成交、滑價 0 bps", "最大回撤 -25%(日頻)", "次日開盤(次日開盤成交、滑價 20 bps", "(期末)", "年化比「收盤」-1.50 個百分點", "不會自動下單"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少「%s」:%s", want, text)
		}
	}
}

func TestCompareSchemaRequiresScenarios(t *testing.T) {
	schema := screenBacktestCompareSchema()
	if !strings.Contains(strings.Join(schema.Required, ","), "scenarios") {
		t.Fatal("scenarios 必填")
	}
	items := schema.Properties["scenarios"]
	if *items.MinItems != 2 || *items.MaxItems != 4 {
		t.Fatalf("情境數量限制錯誤")
	}
	if _, ok := items.Items.Properties["execution_price"]; !ok {
		t.Fatal("情境缺 execution_price")
	}
}

// TestAPIClientScreenBacktestCompare 驗證每個情境編碼成一個 scenario 參數並帶上共用條件。
func TestAPIClientScreenBacktestCompare(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stocks/screen/backtest/compare" {
			t.Errorf("路徑錯誤:%s", r.URL.Path)
		}
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"scenarios":[{"label":"a","overrides":"slippage_bps=0","execution_model":"close","slippage_bps":0,"fee_discount":1,"etf_weight":0,"rebalance_offset":0,"summary":{"periods":1},"years":[]}],"disclaimer":"x"}`))
	}))
	defer srv.Close()
	client := NewAPIClient(srv.URL, "key", 5*time.Second)
	envelope, err := client.ScreenBacktestCompare(t.Context(), ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all"},
		[]url.Values{{"slippage_bps": {"0"}}, {"execution_price": {"next_open"}, "label": {"次日"}}})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if got.Get("hold_days") != "20" || len(got["scenario"]) != 2 || got["scenario"][1] != "execution_price=next_open&label=%E6%AC%A1%E6%97%A5" {
		t.Fatalf("查詢參數錯誤:%v", got)
	}
	if len(envelope.Scenarios) != 1 || envelope.Scenarios[0].Label != "a" {
		t.Fatalf("回應解析錯誤:%#v", envelope)
	}
}
