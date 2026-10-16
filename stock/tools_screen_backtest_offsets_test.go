// Package stock 測試 get_screen_backtest_offsets 的 API client、輸入驗證、摘要與註冊。
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

// fakeScreenBacktestOffsetsQuerier 是同時具備基本 Querier 與 ScreenBacktestOffsetsQuerier 的假資料來源。
type fakeScreenBacktestOffsetsQuerier struct {
	fakeQuerier
	got      ScreenBacktestOptions
	envelope *ScreenBacktestOffsetsEnvelope
	err      error
}

func (f *fakeScreenBacktestOffsetsQuerier) ScreenBacktestOffsets(_ context.Context, opt ScreenBacktestOptions) (*ScreenBacktestOffsetsEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleBacktestOffsets 產生兩個偏移(其中一個沒有已滿的期數)的穩健度結果。
func sampleBacktestOffsets() *ScreenBacktestOffsetsEnvelope {
	return &ScreenBacktestOffsetsEnvelope{
		From: "2019-01-02", To: "2026-10-15", Market: "all", HoldDays: 20, Limit: 20, FeeDiscount: ptr(0.28), EtfBenchmark: ptr("0050"),
		Distribution: BacktestOffsetDistribution{
			AnnualizedReturnAfterCosts: &BacktestStat{Min: 13.11, P25: 17.5, Median: 21.73, Mean: 23.06, P75: 28.2, Max: 36.21},
			SharpeRatio:                &BacktestStat{Min: 0.61, P25: 0.8, Median: 0.96, Mean: 1.0, P75: 1.2, Max: 1.43},
			MaxDrawdownAfterCosts:      &BacktestStat{Min: -36.88, P25: -31, Median: -26.63, Mean: -26, P75: -20, Max: -13.33},
			EtfAnnualizedReturn:        &BacktestStat{Min: 29.67, P25: 30, Median: 30.27, Mean: 30.2, P75: 30.5, Max: 30.8},
			BeatEtfReturnRate:          ptr(0.15),
			BeatEtfSharpeRate:          ptr(0.0),
		},
		Offsets: []BacktestOffsetResult{
			{RebalanceOffset: 0, Summary: BacktestSummary{Periods: 94, AnnualizedAfterCosts: ptr(34.85), SharpeRatio: ptr(1.3), MaxDrawdownAfterCosts: ptr(-24.71)}},
			{RebalanceOffset: 1},
		},
	}
}

// TestAPIClientScreenBacktestOffsets 驗證路徑、query string(不帶 rebalance_offset)、欄位解析與錯誤分層。
func TestAPIClientScreenBacktestOffsets(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"from":"2019-01-02","to":"2026-10-15","market":"all","hold_days":20,"limit":20,"fee_discount":0.28,"etf_benchmark":"0050",` +
				`"distribution":{"annualized_return_after_costs":{"min":13.11,"p25":17.5,"median":21.73,"mean":23.06,"p75":28.2,"max":36.21},` +
				`"sharpe_ratio":null,"max_drawdown_after_costs":null,"etf_annualized_return":null,"etf_sharpe_ratio":null,` +
				`"beat_etf_return_rate":0.15,"beat_etf_sharpe_rate":null},` +
				`"offsets":[{"rebalance_offset":0,"summary":{"periods":94,"invested_periods":66,"annualized_return_after_costs":34.85,"sharpe_ratio":1.3}}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all", MinRSScore: 80, MinAvgTradeValue: 50, RebalanceOffset: 3}
	envelope, err := client.ScreenBacktestOffsets(t.Context(), opt)
	if err != nil {
		t.Fatalf("ScreenBacktestOffsets() 不應失敗:%v", err)
	}
	if gotPath != "/api/v1/stocks/screen/backtest/offsets" || gotQuery.Encode() != "hold_days=20&limit=20&market=all&min_avg_trade_value=50&min_rs_score=80" {
		t.Fatalf("路徑或 query 錯誤(不應帶 rebalance_offset):%s?%s", gotPath, gotQuery.Encode())
	}
	d := envelope.Distribution
	if d.AnnualizedReturnAfterCosts == nil || d.AnnualizedReturnAfterCosts.Median != 21.73 || d.SharpeRatio != nil || *d.BeatEtfReturnRate != 0.15 || d.BeatEtfSharpeRate != nil {
		t.Fatalf("分布解析錯誤:%#v", d)
	}
	if len(envelope.Offsets) != 1 || envelope.Offsets[0].Summary.Periods != 94 || *envelope.Offsets[0].Summary.AnnualizedAfterCosts != 34.85 || *envelope.EtfBenchmark != "0050" {
		t.Fatalf("偏移彙總解析錯誤:%#v", envelope)
	}
	opt.Market = "twse"
	if _, err := client.ScreenBacktestOffsets(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	opt.Market = "tpex"
	if _, err := client.ScreenBacktestOffsets(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
}

// TestScreenBacktestOffsetsTool 驗證參數整理(忽略 rebalance_offset)、驗證、錯誤分層與 structured output。
func TestScreenBacktestOffsetsTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeScreenBacktestOffsetsQuerier) *screenBacktestOffsetsToolset {
		return &screenBacktestOffsetsToolset{oq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}
	f := &fakeScreenBacktestOffsetsQuerier{envelope: sampleBacktestOffsets()}
	result, output, err := newToolset(f).screenBacktestOffsets(t.Context(), nil, ScreenBacktestInput{
		MinRSScore: 80, MinForeignStreak: 3, MarketFilterMA: 60, RebalanceOffset: 99, MinAvgTradeValue: 50,
	})
	if err != nil {
		t.Fatalf("screenBacktestOffsets 不應失敗:%v", err)
	}
	if f.got != (ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all", MinRSScore: 80, MinForeignStreak: 3, MarketFilterMA: 60, MinAvgTradeValue: 50}) {
		t.Fatalf("參數整理錯誤(rebalance_offset 應忽略):%#v", f.got)
	}
	if out, ok := output.(ScreenBacktestOffsetsOutput); !ok || out.DataKind != "screen_backtest_offsets" || out.IsRealtime || len(out.Offsets) != 2 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "年化報酬:中位數 21.73%") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}
	for _, bad := range []ScreenBacktestInput{{HoldDays: 10}, {Limit: 101}, {MinAvgTradeValue: 100001}, {Market: "otc"}} {
		if _, _, err := newToolset(f).screenBacktestOffsets(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}
	if _, _, err := newToolset(&fakeScreenBacktestOffsetsQuerier{err: ErrMarketDataNotFound}).screenBacktestOffsets(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != "指定迄日之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestOffsetsQuerier{err: ErrIndustryNotFound}).screenBacktestOffsets(t.Context(), nil, ScreenBacktestInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestOffsetsQuerier{err: errors.New("boom")}).screenBacktestOffsets(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestScreenBacktestOffsetsSummary 驗證設定說明、分布、勝過 0050 比例、各偏移與沒有資料的說明。
func TestScreenBacktestOffsetsSummary(t *testing.T) {
	opt := ScreenBacktestOptions{
		HoldDays: 20, Limit: 20, Market: "all", Industry: "半導體業", MinRSScore: 80, MinForeignStreak: 3, MinTrustStreak: 2,
		Indicator: "52_week", Kind: "new_high", MinAvgTradeValue: 50,
		MarketFilterMA: 60, MarketBreadth: 30, HoldMinRSScore: 70, MaxPerIndustry: 4, KeepRank: 40,
	}
	summary := screenBacktestOffsetsSummary(sampleBacktestOffsets(), opt)
	for _, want := range []string{
		"all 市場 2019-01-02～2026-10-15 換股日偏移穩健度:每 20 個交易日換股、取 RS 最高的前 20 檔,條件 產業「半導體業」、RS 分數 ≥ 80、外資已連買 3 天以上、投信已連買 2 天以上、換股前出現 52 週 新高、近 20 個交易日平均成交金額 ≥ 50 百萬元;大盤濾網 60 日均線、市場廣度濾網 30%、續抱 RS ≥ 70、同產業最多 4 檔、換股緩衝前 40 名;把換股日偏移(起日之後先跳過幾個交易日才第一次換股)2 種都跑一遍,扣交易成本(手續費折扣 0.28)後:",
		"\n- 年化報酬:中位數 21.73%(四分位 17.5%～28.2%,最低 13.11%、最高 36.21%,平均 23.06%)",
		"\n- 夏普值:中位數 0.96(四分位 0.8～1.2,最低 0.61、最高 1.43,平均 1)",
		"\n- 最大回撤:中位數 -26.63%",
		"\n- 同期 0050(同樣的換股日進出、未扣成本)年化報酬:中位數 30.27%",
		";夏普值:無資料",
		"\n年化報酬勝過同偏移 0050 的比例 15%、夏普值勝過 0050 的比例 0%。",
		"\n各偏移(扣成本):偏移 0:年化 34.85%、夏普 1.3、回撤 -24.71%;偏移 1:年化 無、夏普 無、回撤 無。",
		"中位數比任何單一偏移更能代表這組條件",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	plain := screenBacktestOffsetsSummary(&ScreenBacktestOffsetsEnvelope{From: "2026-10-01", To: "2026-10-15", Market: "twse", HoldDays: 5, Limit: 5},
		ScreenBacktestOptions{HoldDays: 5, Limit: 5, KeepRank: 5})
	if !strings.Contains(plain, "每 5 個交易日換股、取 RS 最高的前 5 檔;把換股日偏移") || strings.Contains(plain, "條件 ") || strings.Contains(plain, "各偏移(扣成本)") ||
		!strings.Contains(plain, "年化報酬:無資料") {
		t.Errorf("沒有條件與偏移時的摘要錯誤:%s", plain)
	}
}

// TestScreenBacktestOffsetsToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀,schema 不收 rebalance_offset。
func TestScreenBacktestOffsetsToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeScreenBacktestOffsetsQuerier{}, nil)
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
		if tool.Name == "get_screen_backtest_offsets" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_screen_backtest_offsets 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_screen_backtest_offsets")
	}
	schema := screenBacktestOffsetsSchema()
	if _, ok := schema.Properties["rebalance_offset"]; ok {
		t.Error("穩健度工具不應公開 rebalance_offset")
	}
	if _, ok := screenBacktestSchema().Properties["rebalance_offset"]; !ok {
		t.Error("刪除 rebalance_offset 不可影響 get_screen_backtest 的 schema")
	}
	if *schema.Properties["min_avg_trade_value"].Maximum != 100000 {
		t.Error("min_avg_trade_value 上限應為 100000")
	}
}
