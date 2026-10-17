// Package stock 測試期間停損、具名策略、報酬歸因與滾動前推工具。
package stock

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

// fakeRound31Querier 同時提供歸因、滾動前推與策略清單。
type fakeRound31Querier struct {
	fakeQuerier
	got         ScreenBacktestOptions
	trainYears  int
	attribution *ScreenBacktestAttributionEnvelope
	walk        *ScreenBacktestWalkForwardEnvelope
	presets     *StrategyPresetsEnvelope
	err         error
}

func (f *fakeRound31Querier) ScreenBacktestAttribution(_ context.Context, opt ScreenBacktestOptions) (*ScreenBacktestAttributionEnvelope, error) {
	f.got = opt
	return f.attribution, f.err
}

func (f *fakeRound31Querier) ScreenBacktestWalkForward(_ context.Context, opt ScreenBacktestOptions, trainYears int) (*ScreenBacktestWalkForwardEnvelope, error) {
	f.got, f.trainYears = opt, trainYears
	return f.walk, f.err
}

func (f *fakeRound31Querier) StrategyPresets(context.Context) (*StrategyPresetsEnvelope, error) {
	return f.presets, f.err
}

// TestScreenBacktestOptionsPresetAndStops 驗證 preset 不套預設值、只帶有指定的參數,停損參數的範圍與 query。
func TestScreenBacktestOptionsPresetAndStops(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{Preset: " 最佳組合 ", RebalanceOffset: 30, StopLoss: 10})
	if err != nil {
		t.Fatalf("preset 不應失敗:%v", err)
	}
	if opt.Preset != "最佳組合" || opt.HoldDays != 0 || opt.Limit != 0 || opt.Market != "" {
		t.Fatalf("preset 不應套預設值:%#v", opt)
	}
	if got := opt.query().Encode(); got != "preset=%E6%9C%80%E4%BD%B3%E7%B5%84%E5%90%88&rebalance_offset=30&stop_loss=10" {
		t.Fatalf("preset 的 query 錯誤:%s", got)
	}
	opt, err = screenBacktestOptions(ScreenBacktestInput{Preset: "a", HoldDays: 60, Limit: 5, Market: "twse", KeepRank: 10, TrailingStop: 8})
	if err != nil {
		t.Fatalf("preset 加覆蓋參數不應失敗:%v", err)
	}
	if got := opt.query().Encode(); got != "hold_days=60&keep_rank=10&limit=5&market=twse&preset=a&trailing_stop=8" {
		t.Fatalf("覆蓋參數的 query 錯誤:%s", got)
	}
	plain, err := screenBacktestOptions(ScreenBacktestInput{})
	if err != nil || plain.query().Encode() != "hold_days=20&limit=20&market=all" {
		t.Fatalf("沒有 preset 仍要帶預設值:%v %s", err, plain.query().Encode())
	}
	for _, bad := range []ScreenBacktestInput{
		{StopLoss: 51}, {StopLoss: -1}, {TrailingStop: 60}, {Preset: strings.Repeat("甲", 65)},
		{Preset: "a", RebalanceOffset: 240}, {Preset: "a", HoldDays: 7}, {Preset: "a", Limit: 101},
		{Preset: "a", Market: "otc"}, {HoldDays: 5, RebalanceOffset: 5},
	} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	if err := backtestToolError(ErrMarketDataNotFound, "x", ScreenBacktestOptions{Preset: "甲"}, nil); err == nil || !strings.Contains(err.Error(), "找不到具名策略「甲」") {
		t.Fatalf("preset 的 404 應提示策略名稱:%v", err)
	}
}

// TestBacktestSummaryShowsPresetAndStops 驗證單次回測摘要列出具名策略、停損設定、停損檔數,設定說明也帶停損與 preset。
func TestBacktestSummaryShowsPresetAndStops(t *testing.T) {
	envelope := &ScreenBacktestEnvelope{
		Preset: ptr("最佳組合"), From: "2024-01-02", To: "2024-03-01", Market: "all", HoldDays: 20, Limit: 2,
		StopLoss: ptr(10), TrailingStop: ptr(15),
		Summary: BacktestSummary{Periods: 1, InvestedPeriods: 1, StopExits: ptr(1)},
		Periods: []BacktestPeriod{{Date: "2024-01-02", Holdings: 2, AvgReturn: ptr(-5.0), Stopped: ptr(1), Symbols: []string{"2330", "2317"}}},
	}
	summary := screenBacktestSummary(envelope)
	for _, want := range []string{
		"具名策略「最佳組合」:all 市場",
		";期間停損 10%、移動停利 15%(每天以還原收盤檢查",
		"期間停損提前賣出共 1 檔次。",
		"- 2024-01-02 持有 2 檔(停損賣出 1 檔),報酬 -5%",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if stopText(nil, ptr(0)) != "" {
		t.Error("沒有停損時應為空字串")
	}
	text := backtestOptionsText(ScreenBacktestOptions{Preset: "甲", HoldDays: 60, Limit: 5, StopLoss: 8})
	if !strings.HasPrefix(text, "具名策略「甲」,每 60 個交易日換股,取前 5 檔") || !strings.Contains(text, "期間停損 8%") {
		t.Errorf("設定說明錯誤:%s", text)
	}
	if text := backtestOptionsText(ScreenBacktestOptions{Preset: "乙"}); text != "具名策略「乙」" {
		t.Errorf("只有 preset 的設定說明錯誤:%s", text)
	}
}

// TestAPIClientRound31Endpoints 驗證歸因、滾動前推與策略清單的路徑、query 與解析。
func TestAPIClientRound31Endpoints(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		if gotQuery.Get("market") == "twse" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		switch r.URL.Path {
		case "/api/v1/stocks/screen/backtest/attribution":
			_, _ = w.Write([]byte(`{"from":"2019-01-02","to":"2026-10-16","market":"all","hold_days":20,"limit":20,"rebalance_offset":0,"benchmark_size":50,` +
				`"summary":{"periods":2,"strategy_return":14,"benchmark_return":6.04,"etf_return":4,"excess_return":7.96,"allocation":0,"selection":2.96,"interaction":5},` +
				`"years":[],"industries":[{"industry":"半導體業","avg_weight":50,"benchmark_avg_weight":100,"allocation":0,"selection":2.96,"interaction":0,"total":2.96}]}`))
		case "/api/v1/stocks/screen/backtest/walk-forward":
			_, _ = w.Write([]byte(`{"from":"2019-01-02","to":"2026-10-16","market":"all","hold_days":20,"limit":20,"train_years":2,"candidates":36,"rebalance_offset":0,` +
				`"years":[{"year":2022,"etf_weight":50,"max_weight":null,"keep_rank":30,"train_sharpe_ratio":1.2,"periods":12,"return_after_costs":-10,"baseline_return_after_costs":-12,"etf_return":-20}],` +
				`"summary":{"periods":12},"baseline":{"periods":12},"offsets":{},"baseline_offsets":{}}`))
		case "/api/v1/strategy-presets":
			_, _ = w.Write([]byte(`{"presets":[{"name":"最佳組合","query":"limit=20","description":"","alert":true}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	opt := ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all", EtfWeight: 50, Split: "2023-01-02"}

	attribution, err := client.ScreenBacktestAttribution(t.Context(), opt)
	if err != nil || gotPath != "/api/v1/stocks/screen/backtest/attribution" || gotQuery.Encode() != "hold_days=20&limit=20&market=all" {
		t.Fatalf("歸因的路徑或 query 錯誤(不帶 etf_weight 與 split):%v %s?%s", err, gotPath, gotQuery.Encode())
	}
	if attribution.BenchmarkSize != 50 || attribution.Summary.Selection != 2.96 || *attribution.Summary.EtfReturn != 4 || attribution.Industries[0].Industry != "半導體業" {
		t.Fatalf("歸因解析錯誤:%#v", attribution)
	}
	walk, err := client.ScreenBacktestWalkForward(t.Context(), opt, 2)
	if err != nil || gotPath != "/api/v1/stocks/screen/backtest/walk-forward" || gotQuery.Encode() != "etf_weight=50&hold_days=20&limit=20&market=all&train_years=2" {
		t.Fatalf("滾動前推的路徑或 query 錯誤(不帶 split):%v %s?%s", err, gotPath, gotQuery.Encode())
	}
	if walk.Candidates != 36 || len(walk.Years) != 1 || walk.Years[0].KeepRank != 30 || walk.Years[0].MaxWeight != nil {
		t.Fatalf("滾動前推解析錯誤:%#v", walk)
	}
	if _, err := client.ScreenBacktestWalkForward(t.Context(), opt, 0); err != nil || gotQuery.Has("train_years") {
		t.Fatalf("train_years 為 0 時不帶:%v %s", err, gotQuery.Encode())
	}
	presets, err := client.StrategyPresets(t.Context())
	if err != nil || gotPath != "/api/v1/strategy-presets" || len(presets.Presets) != 1 || !presets.Presets[0].Alert {
		t.Fatalf("策略清單錯誤:%v %#v", err, presets)
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if _, err := NewAPIClient(broken.URL, "secret", time.Second).StrategyPresets(t.Context()); err == nil {
		t.Fatal("策略清單 500 應回錯誤")
	}
	opt.Market = "twse"
	if _, err := client.ScreenBacktestAttribution(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("歸因 404 應回 ErrMarketDataNotFound:%v", err)
	}
	if _, err := client.ScreenBacktestWalkForward(t.Context(), opt, 0); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("滾動前推 404 應回 ErrMarketDataNotFound:%v", err)
	}
}

func sampleAttribution() *ScreenBacktestAttributionEnvelope {
	effect := func(name string, total float64) BacktestIndustryEffect {
		return BacktestIndustryEffect{Industry: name, AvgWeight: 10, BenchmarkAvgWeight: 20, Allocation: total / 2, Selection: total / 2, Total: total}
	}
	var industries []BacktestIndustryEffect
	for i, name := range []string{"半導體業", "電子零組件業", "航運業", "金融保險業", "現金", "電腦及週邊設備業", "其他"} {
		industries = append(industries, effect(name, float64(6-2*i)))
	}
	totals := BacktestAttributionTotals{Periods: 90, StrategyReturn: 120, BenchmarkReturn: 130, EtfReturn: ptr(140.0), ExcessReturn: -10, Allocation: -12, Selection: 3, Interaction: -1}
	return &ScreenBacktestAttributionEnvelope{
		From: "2019-01-02", To: "2026-10-16", Market: "all", HoldDays: 20, Limit: 20, BenchmarkSize: 50, Summary: totals,
		Years:      []BacktestAttributionYear{{Year: 2024, Totals: totals, Industries: industries[:1]}, {Year: 2025, Totals: totals}},
		Industries: industries,
	}
}

// TestScreenBacktestAttributionTool 驗證參數整理(忽略 etf_weight 與 split)、錯誤、摘要與 structured output。
func TestScreenBacktestAttributionTool(t *testing.T) {
	var logged []string
	toolset := func(f *fakeRound31Querier) *screenBacktestAttributionToolset {
		return &screenBacktestAttributionToolset{aq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeRound31Querier{attribution: sampleAttribution()}
	result, output, err := toolset(f).screenBacktestAttribution(t.Context(), nil, ScreenBacktestInput{EtfWeight: 50, Split: "2023-01-02", MinRSScore: 80})
	if err != nil {
		t.Fatalf("歸因不應失敗:%v", err)
	}
	if f.got.EtfWeight != 0 || f.got.Split != "" || f.got.MinRSScore != 80 {
		t.Fatalf("參數整理錯誤:%#v", f.got)
	}
	if out, ok := output.(ScreenBacktestAttributionOutput); !ok || out.DataKind != "screen_backtest_attribution" || out.BenchmarkSize != 50 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"整段:90 期,策略 +120.00、基準 +130.00、0050 +140.00,超額 -10.00 = 配置 -12.00 + 選股 +3.00 + 交互 -1.00。",
		"\n- 2024 年:90 期", ";主要:半導體業 合計 +6.00",
		"貢獻最多的產業:\n- 半導體業 合計 +6.00(配置 +3.00、選股 +3.00、交互 +0.00;平均權重 10.00% vs 基準 20.00%)",
		"拖累最多的產業:\n- 其他 合計 -6.00",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	empty := screenBacktestAttributionSummary(&ScreenBacktestAttributionEnvelope{BenchmarkSize: 50}, ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	if !strings.Contains(empty, "期間內沒有可歸因的期數。") {
		t.Errorf("沒有期數時的摘要錯誤:%s", empty)
	}
	few := sampleAttribution()
	few.Industries = few.Industries[:2]
	if text := screenBacktestAttributionSummary(few, ScreenBacktestOptions{}); strings.Contains(text, "拖累最多") {
		t.Errorf("產業不多時不分拖累最多:%s", text)
	}
	if _, _, err := toolset(f).screenBacktestAttribution(t.Context(), nil, ScreenBacktestInput{HoldDays: 7}); err == nil {
		t.Error("hold_days 7 應回錯誤")
	}
	if _, _, err := toolset(&fakeRound31Querier{err: errors.New("boom")}).screenBacktestAttribution(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
	if _, ok := screenBacktestAttributionSchema().Properties["etf_weight"]; ok {
		t.Error("歸因工具不應公開 etf_weight")
	}
}

// TestScreenBacktestWalkForwardTool 驗證訓練年數驗證與傳遞、摘要與 structured output。
func TestScreenBacktestWalkForwardTool(t *testing.T) {
	walk := &ScreenBacktestWalkForwardEnvelope{
		From: "2019-01-02", To: "2026-10-16", Market: "all", HoldDays: 20, Limit: 20, FeeDiscount: ptr(0.28), TrainYears: 3, Candidates: 36,
		Years: []BacktestWalkForwardYear{
			{Year: 2022, EtfWeight: 50, MaxWeight: ptr(10), KeepRank: 30, TrainSharpeRatio: 1.25, Periods: 12, ReturnAfterCosts: -10, BaselineReturnAfterCosts: -12, EtfReturn: ptr(-20.0)},
			{Year: 2023, EtfWeight: 0, KeepRank: 20, TrainSharpeRatio: 0.8, Periods: 12, ReturnAfterCosts: 30, BaselineReturnAfterCosts: 25},
		},
		Summary:  BacktestSummary{Periods: 24, AnnualizedAfterCosts: ptr(8.5), SharpeRatio: ptr(0.6)},
		Baseline: BacktestSummary{Periods: 24, AnnualizedAfterCosts: ptr(6.0)},
		Offsets:  BacktestOffsetDistribution{AnnualizedReturnAfterCosts: &BacktestStat{Median: 7.5}, SharpeRatio: &BacktestStat{Median: 0.55}, BeatEtfReturnRate: ptr(0.25)},
	}
	f := &fakeRound31Querier{walk: walk}
	ts := &screenBacktestWalkForwardToolset{wq: f, logf: func(string, ...any) {}}
	in := ScreenBacktestWalkForwardInput{ScreenBacktestInput: ScreenBacktestInput{Split: "2023-01-02", Preset: "最佳組合"}, TrainYears: 2}
	result, output, err := ts.screenBacktestWalkForward(t.Context(), nil, in)
	if err != nil {
		t.Fatalf("滾動前推不應失敗:%v", err)
	}
	if f.trainYears != 2 || f.got.Split != "" || f.got.Preset != "最佳組合" {
		t.Fatalf("參數傳遞錯誤:%d %#v", f.trainYears, f.got)
	}
	if out, ok := output.(ScreenBacktestWalkForwardOutput); !ok || out.DataKind != "screen_backtest_walk_forward" || out.Candidates != 36 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"具名策略「最佳組合」;每年年初只用之前 3 年的期數,在 36 組參數",
		"\n- 2022 年:0050 50%、單檔上限 10%、緩衝前 30 名(訓練夏普 1.25)→ 12 期扣成本 -10%(不調參數 -12%、0050 -20%)",
		"\n- 2023 年:0050 0%、單檔上限 無、緩衝前 20 名",
		"串起樣本外年份:滾動調參數 24 期,年化 8.5%、夏普 0.6",
		"同樣年份不調參數:24 期,年化 6%",
		"滾動 年化 7.5%、夏普 0.55(勝過 0050 的偏移比例:報酬 25%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	if empty := screenBacktestWalkForwardSummary(&ScreenBacktestWalkForwardEnvelope{TrainYears: 3}, ScreenBacktestOptions{}); !strings.Contains(empty, "沒有可套用的年份") {
		t.Errorf("沒有年份時的摘要錯誤:%s", empty)
	}
	for _, bad := range []ScreenBacktestWalkForwardInput{{TrainYears: 6}, {TrainYears: -1}, {ScreenBacktestInput: ScreenBacktestInput{Limit: 101}}} {
		if _, _, err := ts.screenBacktestWalkForward(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	failing := &screenBacktestWalkForwardToolset{wq: &fakeRound31Querier{err: ErrMarketDataNotFound}, logf: func(string, ...any) {}}
	if _, _, err := failing.screenBacktestWalkForward(t.Context(), nil, ScreenBacktestWalkForwardInput{}); err == nil || err.Error() != "指定迄日之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if schema := screenBacktestWalkForwardSchema(); schema.Properties["train_years"] == nil || screenBacktestSchema().Properties["train_years"] != nil {
		t.Error("只有滾動前推工具公開 train_years")
	}
}

// TestStrategyPresetsTool 驗證策略清單的摘要(含通知標記、說明)、空清單與錯誤。
func TestStrategyPresetsTool(t *testing.T) {
	var logged []string
	ts := &strategyPresetsToolset{pq: &fakeRound31Querier{presets: &StrategyPresetsEnvelope{Presets: []StrategyPreset{
		{Name: "最佳組合", Query: "limit=20&from=2019-01-02", Description: "成交金額排序", Alert: true},
		{Name: "測試", Query: "limit=5"},
	}}}, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	result, output, err := ts.strategyPresets(t.Context(), nil, struct{}{})
	if err != nil {
		t.Fatalf("策略清單不應失敗:%v", err)
	}
	if out, ok := output.(StrategyPresetsOutput); !ok || out.DataKind != "strategy_presets" || len(out.Presets) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{
		"具名策略 2 個",
		"\n- 最佳組合【每個交易日 22:10 發換股通知】:成交金額排序\n  條件 limit=20&from=2019-01-02",
		"\n- 測試\n  條件 limit=5",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	if text := strategyPresetsSummary(&StrategyPresetsEnvelope{}); !strings.Contains(text, "目前沒有具名策略") {
		t.Errorf("空清單的摘要錯誤:%s", text)
	}
	failing := &strategyPresetsToolset{pq: &fakeRound31Querier{err: errors.New("boom")}, logf: ts.logf}
	if _, _, err := failing.strategyPresets(t.Context(), nil, struct{}{}); err == nil || err.Error() != errInternal || len(logged) != 1 {
		t.Fatalf("內部錯誤不可外洩細節:%v %v", err, logged)
	}
}

// TestRound31ToolsAreRegisteredReadOnly 經由 tools/list 驗證三個新工具都有註冊且為唯讀。
func TestRound31ToolsAreRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeRound31Querier{}, nil)
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
	want := map[string]bool{"get_screen_backtest_attribution": false, "get_screen_backtest_walk_forward": false, "list_strategy_presets": false}
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatalf("列出工具:%v", err)
		}
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("%s 沒有註冊或不是唯讀", name)
		}
	}
}
