// Package stock 測試 get_screen_backtest_frontier 的 API client、輸入驗證、摘要與註冊。
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

// fakeScreenBacktestFrontierQuerier 是同時具備基本 Querier 與 ScreenBacktestFrontierQuerier 的假資料來源。
type fakeScreenBacktestFrontierQuerier struct {
	fakeQuerier
	got      ScreenBacktestOptions
	envelope *ScreenBacktestFrontierEnvelope
	err      error
}

func (f *fakeScreenBacktestFrontierQuerier) ScreenBacktestFrontier(_ context.Context, opt ScreenBacktestOptions) (*ScreenBacktestFrontierEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleBacktestFrontier 產生 0%、50%、100% 三個比例的前緣結果。
func sampleBacktestFrontier() *ScreenBacktestFrontierEnvelope {
	point := func(weight int, annualized, sharpe, drawdown float64) BacktestFrontierPoint {
		return BacktestFrontierPoint{EtfWeight: weight, Distribution: BacktestOffsetDistribution{
			AnnualizedReturnAfterCosts: &BacktestStat{Median: annualized},
			SharpeRatio:                &BacktestStat{Median: sharpe},
			MaxDrawdownAfterCosts:      &BacktestStat{Median: drawdown},
			Volatility:                 &BacktestStat{Median: 20},
			EtfAnnualizedReturn:        &BacktestStat{Median: 30.27},
			EtfSharpeRatio:             &BacktestStat{Median: 1.3},
			EtfMaxDrawdown:             &BacktestStat{Median: -28.5},
			BeatEtfReturnRate:          ptr(0.3),
			BeatEtfSharpeRate:          ptr(0.8),
		}}
	}
	return &ScreenBacktestFrontierEnvelope{
		From: "2019-01-02", To: "2026-10-15", Market: "all", HoldDays: 20, Limit: 20, FeeDiscount: ptr(0.28), EtfBenchmark: ptr("0050"),
		Offsets: 20, BestSharpeWeight: ptr(50), BestReturnWeight: ptr(100),
		Weights: []BacktestFrontierPoint{point(0, 27.47, 1.18, -23.03), point(50, 29.67, 1.46, -21.05), point(100, 30.27, 1.3, -28.5)},
	}
}

// TestAPIClientScreenBacktestFrontier 驗證路徑、query string(不帶 rebalance_offset 與 etf_weight)、欄位解析與錯誤分層。
func TestAPIClientScreenBacktestFrontier(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("market") {
		case "twse":
			w.WriteHeader(http.StatusNotFound)
		case "tpex":
			w.WriteHeader(http.StatusUnprocessableEntity)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"from":"2019-01-02","to":"2026-10-15","market":"all","hold_days":20,"limit":20,"rank_by":"trade_value","weighting":"equal",` +
				`"fee_discount":0.28,"etf_benchmark":"0050","offsets":20,"best_sharpe_weight":50,"best_return_weight":null,` +
				`"weights":[{"etf_weight":0,"distribution":{"annualized_return_after_costs":{"min":1,"p25":2,"median":3,"mean":3,"p75":4,"max":5},` +
				`"sharpe_ratio":null,"max_drawdown_after_costs":null,"etf_annualized_return":null,"etf_sharpe_ratio":null,"volatility":null,` +
				`"etf_volatility":null,"etf_max_drawdown":null,"beat_etf_return_rate":0.2,"beat_etf_sharpe_rate":null,"beat_etf_drawdown_rate":null}}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all", MinRSScore: 80, RebalanceOffset: 3, EtfWeight: 40, RankBy: "trade_value"}
	envelope, err := client.ScreenBacktestFrontier(t.Context(), opt)
	if err != nil {
		t.Fatalf("ScreenBacktestFrontier() 不應失敗:%v", err)
	}
	if gotPath != "/api/v1/stocks/screen/backtest/frontier" || gotQuery.Encode() != "hold_days=20&limit=20&market=all&min_rs_score=80&rank_by=trade_value" {
		t.Fatalf("路徑或 query 錯誤(不應帶 rebalance_offset 與 etf_weight):%s?%s", gotPath, gotQuery.Encode())
	}
	if envelope.Offsets != 20 || *envelope.BestSharpeWeight != 50 || envelope.BestReturnWeight != nil || *envelope.Weighting != "equal" ||
		len(envelope.Weights) != 1 || envelope.Weights[0].Distribution.AnnualizedReturnAfterCosts.Median != 3 || *envelope.Weights[0].Distribution.BeatEtfReturnRate != 0.2 {
		t.Fatalf("前緣解析錯誤:%#v", envelope)
	}
	opt.Market = "twse"
	if _, err := client.ScreenBacktestFrontier(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	opt.Market = "tpex"
	if _, err := client.ScreenBacktestFrontier(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
}

// TestScreenBacktestFrontierTool 驗證參數整理(忽略 rebalance_offset 與 etf_weight)、驗證、錯誤分層與 structured output。
func TestScreenBacktestFrontierTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeScreenBacktestFrontierQuerier) *screenBacktestFrontierToolset {
		return &screenBacktestFrontierToolset{fq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeScreenBacktestFrontierQuerier{envelope: sampleBacktestFrontier()}
	result, output, err := newToolset(f).screenBacktestFrontier(t.Context(), nil, ScreenBacktestInput{
		MinRSScore: 80, MinForeignStreak: 3, RebalanceOffset: 9, EtfWeight: 70, RankBy: "trade_value", Weighting: "trade_value",
	})
	if err != nil {
		t.Fatalf("screenBacktestFrontier 不應失敗:%v", err)
	}
	if f.got != (ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all", MinRSScore: 80, MinForeignStreak: 3, RankBy: "trade_value", Weighting: "trade_value"}) {
		t.Fatalf("參數整理錯誤(rebalance_offset 與 etf_weight 應忽略):%#v", f.got)
	}
	if out, ok := output.(ScreenBacktestFrontierOutput); !ok || out.DataKind != "screen_backtest_frontier" || out.IsRealtime || len(out.Weights) != 3 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "夏普值中位數最高的比例:50%") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}
	for _, bad := range []ScreenBacktestInput{{HoldDays: 10}, {Limit: 101}, {Weighting: "cap"}, {Market: "otc"}} {
		if _, _, err := newToolset(f).screenBacktestFrontier(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	if _, _, err := newToolset(&fakeScreenBacktestFrontierQuerier{err: ErrMarketDataNotFound}).screenBacktestFrontier(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != "指定迄日之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestFrontierQuerier{err: ErrIndustryNotFound}).screenBacktestFrontier(t.Context(), nil, ScreenBacktestInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestFrontierQuerier{err: errors.New("boom")}).screenBacktestFrontier(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestScreenBacktestFrontierSummary 驗證設定說明、各比例中位數、0050 對照、最佳比例與沒有資料時的說明。
func TestScreenBacktestFrontierSummary(t *testing.T) {
	summary := screenBacktestFrontierSummary(sampleBacktestFrontier(), ScreenBacktestOptions{
		HoldDays: 20, Limit: 20, Market: "all", MinRSScore: 80, RankBy: "trade_value", MarketFilterMA: 60,
	})
	for _, want := range []string{
		"all 市場 2019-01-02～2026-10-15 0050 核心部位前緣:每 20 個交易日換股、取近 20 個交易日平均成交金額最高的前 20 檔,條件 RS 分數 ≥ 80;大盤濾網 60 日均線;每個比例都把換股日偏移 20 種跑一遍,扣交易成本(手續費折扣 0.28)後各偏移的中位數:",
		"\n- 0050 0%:年化 27.47%、夏普 1.18、最大回撤 -23.03%、波動率 20%;勝過 0050 的偏移比例:報酬 30%、夏普 80%",
		"\n- 0050 50%:年化 29.67%、夏普 1.46、最大回撤 -21.05%",
		"\n同期 0050(同樣的換股日進出、未扣成本)中位數:年化 30.27%、夏普 1.3、最大回撤 -28.5%。",
		"\n夏普值中位數最高的比例:50%;年化報酬中位數最高的比例:100%。",
		"0% 為純策略、100% 等於只持有 0050;每期調回固定比例的買賣計入成本(0050 證交稅 0.1%,第一次買進 0050 不計)。",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	empty := screenBacktestFrontierSummary(&ScreenBacktestFrontierEnvelope{From: "2026-10-01", To: "2026-10-15", Market: "twse", HoldDays: 5, Limit: 5},
		ScreenBacktestOptions{HoldDays: 5, Limit: 5})
	if !strings.Contains(empty, "夏普值中位數最高的比例:無;年化報酬中位數最高的比例:無。") || strings.Contains(empty, "同期 0050") {
		t.Errorf("沒有資料時的摘要錯誤:%s", empty)
	}
	nullPoint := screenBacktestFrontierSummary(&ScreenBacktestFrontierEnvelope{Weights: []BacktestFrontierPoint{{EtfWeight: 10}}}, ScreenBacktestOptions{HoldDays: 5, Limit: 5})
	if !strings.Contains(nullPoint, "\n- 0050 10%:年化 無、夏普 無、最大回撤 無、波動率 無;勝過 0050 的偏移比例:報酬 無、夏普 無") {
		t.Errorf("分布缺值時應顯示無:%s", nullPoint)
	}
}

// TestScreenBacktestFrontierToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀,schema 不收 rebalance_offset 與 etf_weight。
func TestScreenBacktestFrontierToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeScreenBacktestFrontierQuerier{}, nil)
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
		if tool.Name == "get_screen_backtest_frontier" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_screen_backtest_frontier 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_screen_backtest_frontier")
	}
	schema := screenBacktestFrontierSchema()
	for _, field := range []string{"rebalance_offset", "etf_weight"} {
		if _, ok := schema.Properties[field]; ok {
			t.Errorf("前緣工具不應公開 %s", field)
		}
	}
	if _, ok := screenBacktestOffsetsSchema().Properties["etf_weight"]; !ok {
		t.Error("刪除 etf_weight 不可影響 get_screen_backtest_offsets 的 schema")
	}
	if _, ok := schema.Properties["weighting"]; !ok {
		t.Error("前緣工具應可設 weighting")
	}
}
