package stock

// 本檔案測試回測工具的成交假設參數:execution_price(close/next_open)與 slippage_bps。

import (
	"strings"
	"testing"
)

// TestBacktestExecutionOptions 驗證成交價模型與滑價會原樣傳給 Data API,並擋下不合法的組合。
func TestBacktestExecutionOptions(t *testing.T) {
	f := &fakeScreenBacktestQuerier{envelope: sampleScreenBacktest()}
	ts := &screenBacktestToolset{bq: f, logf: func(string, ...any) {}}
	if _, _, err := ts.screenBacktest(t.Context(), nil, ScreenBacktestInput{ExecutionPrice: "next_open", SlippageBps: 20}); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if f.got.ExecutionPrice != "next_open" || f.got.SlippageBps != 20 {
		t.Fatalf("成交假設應傳給 API:%#v", f.got)
	}
	query := f.got.query()
	if query.Get("execution_price") != "next_open" || query.Get("slippage_bps") != "20" {
		t.Fatalf("query string 錯誤:%s", query.Encode())
	}
	if q := (ScreenBacktestOptions{}).query(); q.Has("execution_price") || q.Has("slippage_bps") {
		t.Fatalf("未指定時不帶參數,沿用 API 預設:%s", q.Encode())
	}
	for _, bad := range []ScreenBacktestInput{
		{ExecutionPrice: "vwap"}, {ExecutionPrice: "NEXT_OPEN"}, {SlippageBps: 201}, {SlippageBps: -1},
		{ExecutionPrice: "next_open", StopLoss: 10}, {ExecutionPrice: "next_open", TrailingStop: 10},
	} {
		if _, _, err := ts.screenBacktest(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	for _, ok := range []ScreenBacktestInput{{ExecutionPrice: "close", StopLoss: 10}, {SlippageBps: 200}} {
		if _, _, err := ts.screenBacktest(t.Context(), nil, ok); err != nil {
			t.Errorf("%#v 不應回錯誤:%v", ok, err)
		}
	}
}

// TestBacktestExecutionText 驗證條件說明會揭露成交假設;收盤且無滑價時不多寫。
func TestBacktestExecutionText(t *testing.T) {
	base := ScreenBacktestOptions{HoldDays: 20, Limit: 20}
	if text := backtestOptionsText(base); strings.Contains(text, "開盤") || strings.Contains(text, "滑價") {
		t.Fatalf("預設不應揭露成交假設:%s", text)
	}
	base.ExecutionPrice, base.SlippageBps = "next_open", 20
	if text := backtestOptionsText(base); !strings.Contains(text, "次日開盤成交、滑價 20 bps") {
		t.Fatalf("應揭露成交假設:%s", text)
	}
	if text := executionText(ScreenBacktestOptions{SlippageBps: 5}); text != "滑價 5 bps" {
		t.Fatalf("只有滑價:%s", text)
	}
}

// TestBacktestExecutionSchema 驗證四支回測工具的 schema 都帶有成交假設參數。
func TestBacktestExecutionSchema(t *testing.T) {
	for name, schema := range map[string]map[string]bool{
		"backtest":     keys(screenBacktestSchema().Properties),
		"offsets":      keys(screenBacktestOffsetsSchema().Properties),
		"frontier":     keys(screenBacktestFrontierSchema().Properties),
		"walk_forward": keys(screenBacktestWalkForwardSchema().Properties),
	} {
		if !schema["execution_price"] || !schema["slippage_bps"] {
			t.Errorf("%s schema 缺少成交假設參數", name)
		}
	}
	slippage := screenBacktestSchema().Properties["slippage_bps"]
	if *slippage.Minimum != 0 || *slippage.Maximum != 200 {
		t.Fatalf("slippage_bps 範圍錯誤:%v %v", *slippage.Minimum, *slippage.Maximum)
	}
}

func keys[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for key := range m {
		out[key] = true
	}
	return out
}
