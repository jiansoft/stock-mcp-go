// Package stock 測試 get_screen_backtest 的 API client、輸入驗證、摘要與註冊。
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

// fakeScreenBacktestQuerier 是同時具備基本 Querier 與 ScreenBacktestQuerier 的假資料來源。
type fakeScreenBacktestQuerier struct {
	fakeQuerier
	got      ScreenBacktestOptions
	envelope *ScreenBacktestEnvelope
	err      error
}

func (f *fakeScreenBacktestQuerier) ScreenBacktest(_ context.Context, opt ScreenBacktestOptions) (*ScreenBacktestEnvelope, error) {
	f.got = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.envelope, nil
}

// sampleScreenBacktest 產生 8 期(第 7 期空手)的回測結果。
func sampleScreenBacktest() *ScreenBacktestEnvelope {
	periods := make([]BacktestPeriod, 0, 8)
	for i := range 8 {
		periods = append(periods, BacktestPeriod{
			Date: "2026-0" + string(rune('1'+i)) + "-02", Holdings: 20, AvgReturn: ptr(float64(i) - 2),
			BenchmarkReturn: ptr(1.5), Symbols: []string{"2330", "2454"},
		})
	}
	periods[6].Holdings, periods[6].AvgReturn, periods[6].Symbols = 0, nil, []string{}
	periods[7].ReturnAfterCosts, periods[7].Turnover = ptr(4.78), ptr(0.5)
	return &ScreenBacktestEnvelope{
		From: "2023-10-15", To: "2026-10-14", Market: "all", HoldDays: 20, Limit: 20, Adjusted: true,
		Summary: BacktestSummary{
			Periods: 8, InvestedPeriods: 7, AvgHoldings: ptr(20.0), TotalReturn: ptr(35.2), BenchmarkTotalReturn: ptr(12.6),
			AnnualizedReturn: ptr(10.6), BenchmarkAnnualizedReturn: ptr(4.0), MaxDrawdown: ptr(-18.3), BenchmarkMaxDrawdown: ptr(-9.1),
			WinRate: ptr(0.625), BeatMarketRate: ptr(0.5), AvgPeriodReturn: ptr(1.4), AvgBenchmarkReturn: ptr(1.5),
			AvgTurnover: ptr(0.4321), TotalReturnAfterCosts: ptr(30.1), AnnualizedAfterCosts: ptr(9.2), MaxDrawdownAfterCosts: ptr(-19.0),
		},
		FeeDiscount: ptr(0.28),
		Periods:     periods,
	}
}

// TestAPIClientScreenBacktest 驗證路徑、query string、欄位解析與 404／422／500 的分層。
func TestAPIClientScreenBacktest(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		switch gotQuery.Get("market") {
		case "twse":
			w.WriteHeader(http.StatusNotFound)
		case "tpex":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "none":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"from":"2023-10-15","to":"2026-10-14","market":"all","hold_days":20,"limit":20,` +
				`"industry":null,"min_rs_score":null,"max_rs_score":null,"min_foreign_streak":null,"min_trust_streak":null,` +
				`"indicator":null,"kind":null,"fee_discount":0.28,"adjusted":true,"etf_benchmark":"0050",` +
				`"summary":{"periods":1,"invested_periods":0,"avg_holdings":null,"avg_turnover":null,"total_return":0.0,` +
				`"total_return_after_costs":-0.44,"benchmark_total_return":2.5,` +
				`"annualized_return":0.0,"benchmark_annualized_return":35.1,"max_drawdown":0.0,"benchmark_max_drawdown":0.0,` +
				`"win_rate":0.0,"beat_market_rate":0.0,"avg_period_return":0.0,"avg_benchmark_return":2.5,` +
				`"etf_total_return":1.8,"etf_annualized_return":25.3,"etf_max_drawdown":0.0,"beat_etf_rate":0.0},` +
				`"years":[{"year":2026,"periods":1,"total_return":0.0,"total_return_after_costs":-0.44,` +
				`"benchmark_total_return":2.5,"etf_total_return":1.8,"win_rate":0.0,"beat_market_rate":0.0}],` +
				`"rebalance_offset":0,"current":{"date":"2026-10-14","days_elapsed":3,"days_to_next_rebalance":17,` +
				`"market_filtered":false,"turnover":0.25,"holdings":[{"symbol":"2330","name":"台積電"},{"symbol":"79979","name":null}]},` +
				`"contributors":{"held_symbols":2,"best":[{"symbol":"2330","name":"台積電","periods_held":1,"contribution":1.5,"avg_return":3.0}],"worst":[]},` +
				`"periods":[{"date":"2026-09-02","holdings":0,"avg_return":null,"return_after_costs":-0.44,"turnover":0.0,` +
				`"benchmark_return":2.5,"etf_return":1.8,"symbols":[]}]}`))
		}
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)

	opt := ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all"}
	envelope, err := client.ScreenBacktest(t.Context(), opt)
	if err != nil || envelope.Summary.Periods != 1 || envelope.Summary.AvgHoldings != nil || envelope.Periods[0].AvgReturn != nil || *envelope.Periods[0].BenchmarkReturn != 2.5 {
		t.Fatalf("ScreenBacktest() = %#v, %v", envelope, err)
	}
	if *envelope.FeeDiscount != 0.28 || *envelope.Summary.TotalReturnAfterCosts != -0.44 || *envelope.Periods[0].ReturnAfterCosts != -0.44 || *envelope.Periods[0].Turnover != 0 {
		t.Fatalf("成本欄位解析錯誤:%#v", envelope)
	}
	if *envelope.EtfBenchmark != "0050" || *envelope.Summary.EtfTotalReturn != 1.8 || *envelope.Periods[0].EtfReturn != 1.8 ||
		len(envelope.Years) != 1 || envelope.Years[0].Year != 2026 || *envelope.Years[0].EtfTotalReturn != 1.8 {
		t.Fatalf("0050 與年度欄位解析錯誤:%#v", envelope)
	}
	if c := envelope.Contributors; c == nil || c.HeldSymbols != 2 || len(c.Best) != 1 || *c.Best[0].Name != "台積電" || c.Best[0].Contribution != 1.5 || len(c.Worst) != 0 {
		t.Fatalf("個股貢獻解析錯誤:%#v", envelope.Contributors)
	}
	if c := envelope.Current; c == nil || c.Date != "2026-10-14" || c.DaysElapsed != 3 || c.DaysToNextRebalance != 17 || c.Turnover != 0.25 ||
		len(c.Holdings) != 2 || *c.Holdings[0].Name != "台積電" || c.Holdings[1].Name != nil || *envelope.RebalanceOffset != 0 {
		t.Fatalf("目前持股解析錯誤:%#v", envelope.Current)
	}
	if gotPath != "/api/v1/stocks/screen/backtest" || gotQuery.Encode() != "hold_days=20&limit=20&market=all" {
		t.Fatalf("未指定條件不應帶參數:%s?%s", gotPath, gotQuery.Encode())
	}
	opt = ScreenBacktestOptions{
		From: "2019-01-02", To: "2026-10-14", HoldDays: 60, Limit: 5, Market: "twse", Industry: "半導體業",
		MinRSScore: 80, MaxRSScore: 95, MinForeignStreak: 3, MinTrustStreak: 2, Indicator: "52_week", Kind: "new_high",
		FeeDiscount: ptr(0.0), KeepRank: 10, MarketFilterMA: 120, HoldMinRSScore: 60, MaxPerIndustry: 4, MarketBreadth: 40,
		RebalanceOffset: 7, MinAvgTradeValue: 50, RankBy: "trade_value", EtfWeight: 30,
	}
	if _, err := client.ScreenBacktest(t.Context(), opt); !errors.Is(err, ErrMarketDataNotFound) {
		t.Fatalf("404 應回 ErrMarketDataNotFound:%v", err)
	}
	for key, want := range map[string]string{
		"from": "2019-01-02", "to": "2026-10-14", "hold_days": "60", "limit": "5", "industry": "半導體業",
		"min_rs_score": "80", "max_rs_score": "95", "min_foreign_streak": "3", "min_trust_streak": "2",
		"indicator": "52_week", "kind": "new_high", "fee_discount": "0", "keep_rank": "10", "market_filter_ma": "120", "hold_min_rs_score": "60",
		"max_per_industry": "4", "market_filter_breadth": "40", "rebalance_offset": "7", "min_avg_trade_value": "50",
		"rank_by": "trade_value", "etf_weight": "30",
	} {
		if gotQuery.Get(key) != want {
			t.Errorf("query %s = %q,應為 %q", key, gotQuery.Get(key), want)
		}
	}
	opt.Market = "tpex"
	if _, err := client.ScreenBacktest(t.Context(), opt); !errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("422 應回 ErrIndustryNotFound:%v", err)
	}
	opt.Market = "none"
	if _, err := client.ScreenBacktest(t.Context(), opt); err == nil || errors.Is(err, ErrMarketDataNotFound) || errors.Is(err, ErrIndustryNotFound) {
		t.Fatalf("500 應回一般錯誤:%v", err)
	}
}

// TestScreenBacktestTool 驗證預設值、參數整理與檢查、錯誤分層、摘要與 structured output。
func TestScreenBacktestTool(t *testing.T) {
	var logged []string
	newToolset := func(f *fakeScreenBacktestQuerier) *screenBacktestToolset {
		return &screenBacktestToolset{bq: f, logf: func(format string, _ ...any) { logged = append(logged, format) }}
	}

	f := &fakeScreenBacktestQuerier{envelope: sampleScreenBacktest()}
	result, output, err := newToolset(f).screenBacktest(t.Context(), nil, ScreenBacktestInput{})
	if err != nil {
		t.Fatalf("screenBacktest 不應失敗:%v", err)
	}
	if f.got != (ScreenBacktestOptions{HoldDays: 20, Limit: 20, Market: "all"}) {
		t.Fatalf("預設條件錯誤:%#v", f.got)
	}
	if out, ok := output.(ScreenBacktestOutput); !ok || out.DataKind != "screen_backtest" || out.IsRealtime || out.Summary.Periods != 8 {
		t.Fatalf("structured output 錯誤:%#v", output)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "共 8 期") {
		t.Fatalf("摘要內容錯誤:%s", text)
	}

	in := ScreenBacktestInput{
		From: "2019-01-02", To: "2026-10-14", HoldDays: 5, Limit: 100, Market: " TPEX ", Industry: " 半導體業 ",
		MinRSScore: 80, MaxRSScore: 99, MinForeignStreak: 1, MinTrustStreak: 20, Indicator: "52_week", Kind: " new_high ",
		FeeDiscount: ptr(0.0), KeepRank: 200, MarketFilterMA: 240, HoldMinRSScore: 99, MaxPerIndustry: 100, RebalanceOffset: 4, MinAvgTradeValue: 100000,
		RankBy: "foreign_streak", EtfWeight: 100,
	}
	if _, _, err := newToolset(f).screenBacktest(t.Context(), nil, in); err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	want := ScreenBacktestOptions{
		From: "2019-01-02", To: "2026-10-14", HoldDays: 5, Limit: 100, KeepRank: 200, MarketFilterMA: 240, HoldMinRSScore: 99, MaxPerIndustry: 100, RebalanceOffset: 4, MinAvgTradeValue: 100000, Market: "tpex", Industry: "半導體業",
		MinRSScore: 80, MaxRSScore: 99, MinForeignStreak: 1, MinTrustStreak: 20, Indicator: "52_week", Kind: "new_high",
		RankBy: "foreign_streak", EtfWeight: 100,
	}
	if f.got.FeeDiscount == nil || *f.got.FeeDiscount != 0 {
		t.Fatalf("fee_discount=0 應原樣傳給 API:%#v", f.got.FeeDiscount)
	}
	f.got.FeeDiscount = nil
	if f.got != want {
		t.Fatalf("參數應整理後傳給 API:%#v", f.got)
	}
	for _, bad := range []ScreenBacktestInput{
		{From: "2026-13-01"}, {To: "bad"}, {From: "2026-10-14", To: "2026-10-13"}, {From: "2016-10-13", To: "2026-10-14"},
		{HoldDays: 10}, {HoldDays: -5}, {Limit: 101}, {Limit: -1}, {Market: "otc"}, {Industry: strings.Repeat("業", 21)},
		{MinRSScore: 100}, {MinRSScore: 90, MaxRSScore: 80}, {MinForeignStreak: 61}, {Kind: "breakout"}, {Indicator: "ma1"},
		{FeeDiscount: ptr(-0.1)}, {FeeDiscount: ptr(1.5)}, {KeepRank: 19}, {Limit: 5, KeepRank: 4}, {KeepRank: 201}, {MarketFilterMA: 50}, {HoldMinRSScore: 100}, {HoldMinRSScore: -1},
		{MaxPerIndustry: 101}, {MaxPerIndustry: -1},
		{MarketBreadth: 100}, {MarketBreadth: -1},
		{RebalanceOffset: 20}, {RebalanceOffset: -1}, {HoldDays: 5, RebalanceOffset: 5},
		{MinAvgTradeValue: 100001}, {MinAvgTradeValue: -1},
		{RankBy: "volume"}, {RankBy: "RS_SCORE"}, {EtfWeight: 101}, {EtfWeight: -1},
	} {
		if _, _, err := newToolset(f).screenBacktest(t.Context(), nil, bad); err == nil {
			t.Errorf("%#v 應回錯誤", bad)
		}
	}

	if _, _, err := newToolset(&fakeScreenBacktestQuerier{err: ErrMarketDataNotFound}).screenBacktest(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != "指定迄日之前查無相對強度資料" {
		t.Fatalf("404 應是可理解的錯誤:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestQuerier{err: ErrIndustryNotFound}).screenBacktest(t.Context(), nil, ScreenBacktestInput{Industry: "不存在"}); err == nil || !strings.Contains(err.Error(), "找不到產業「不存在」") {
		t.Fatalf("422 應提示產業名稱:%v", err)
	}
	if _, _, err := newToolset(&fakeScreenBacktestQuerier{err: errors.New("boom")}).screenBacktest(t.Context(), nil, ScreenBacktestInput{}); err == nil || err.Error() != errInternal {
		t.Fatalf("內部錯誤不可外洩細節:%v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("只有內部錯誤需要記錄:%v", logged)
	}
}

// TestValidateBacktestPeriod 驗證起訖先後與 3,653 天上限;只給一端時不檢查。
func TestValidateBacktestPeriod(t *testing.T) {
	for _, ok := range [][2]string{{"", ""}, {"2016-10-14", ""}, {"2016-10-14", "2026-10-14"}} {
		if err := validateBacktestPeriod(ok[0], ok[1]); err != nil {
			t.Errorf("%v 不應回錯誤:%v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"2016-10-13", "2026-10-14"}, {"2026-10-15", "2026-10-14"}, {"x", "2026-10-14"}, {"2026-10-14", "y"}} {
		if err := validateBacktestPeriod(bad[0], bad[1]); err == nil {
			t.Errorf("%v 應回錯誤", bad)
		}
	}
}

// TestScreenBacktestSummary 驗證條件說明、彙總、最近 6 期(含空手)與沒有期數的說明。
func TestScreenBacktestSummary(t *testing.T) {
	e := sampleScreenBacktest()
	e.Industry, e.MinRSScore, e.MinTrustStreak, e.Indicator, e.Kind = ptr("半導體業"), ptr(80), ptr(3), ptr("52_week"), ptr("new_high")
	e.EtfBenchmark, e.KeepRank, e.MarketFilterMA, e.HoldMinRSScore, e.MaxPerIndustry = ptr("0050"), ptr(40), ptr(60), ptr(50), ptr(4)
	e.Summary.MarketFilteredPeriods = 1
	e.MarketBreadth = ptr(40)
	e.RebalanceOffset = ptr(3)
	e.MinAvgTradeValue = ptr(50)
	e.EtfWeight = ptr(0)
	e.Current = &BacktestCurrent{
		Date: "2026-10-14", DaysElapsed: 3, DaysToNextRebalance: 17, Turnover: 0.25,
		Holdings: []BacktestHolding{{Symbol: "2330", Name: ptr("台積電")}, {Symbol: "79979"}},
	}
	e.Contributors = &BacktestContributors{
		HeldSymbols: 37,
		Best: []BacktestContributor{
			{Symbol: "2330", Name: ptr("台積電"), PeriodsHeld: 12, Contribution: 8.5, AvgReturn: ptr(14.2)},
			{Symbol: "79979", PeriodsHeld: 1, Contribution: 0.3},
		},
		Worst: []BacktestContributor{{Symbol: "2454", Name: ptr("聯發科"), PeriodsHeld: 3, Contribution: -1.25, AvgReturn: ptr(-8.33)}},
	}
	e.Periods[6].MarketFiltered = true
	e.Summary.Volatility, e.Summary.SharpeRatio, e.Summary.BenchmarkVolatility, e.Summary.BenchmarkSharpeRatio = ptr(24.5), ptr(1.12), ptr(18.0), ptr(0.95)
	e.Summary.EtfVolatility, e.Summary.EtfSharpeRatio = ptr(17.2), ptr(1.31)
	e.Summary.EtfTotalReturn, e.Summary.EtfAnnualizedReturn, e.Summary.EtfMaxDrawdown, e.Summary.BeatEtfRate = ptr(20.5), ptr(6.4), ptr(-12.0), ptr(0.75)
	e.Periods[7].EtfReturn = ptr(2.1)
	e.Years = []BacktestYear{
		{Year: 2025, Periods: 3, TotalReturn: 12.3, TotalReturnAfterCosts: 10.1, BenchmarkTotalReturn: 4.5, WinRate: 0.6667, BeatMarketRate: 1},
		{Year: 2026, Periods: 5, TotalReturn: -3.2, TotalReturnAfterCosts: -5, BenchmarkTotalReturn: 1.1, EtfTotalReturn: ptr(8.8), WinRate: 0.4, BeatMarketRate: 0.2},
	}
	summary := screenBacktestSummary(e)
	for _, want := range []string{
		"all 市場 2023-10-15～2026-10-14 條件選股回測:每 20 個交易日換股,依相對強度分數由高到低取前 20 檔等權持有",
		"條件 產業「半導體業」、換股當天 RS 分數 ≥ 80、投信已連買 3 天以上、換股前 20 個交易日內出現 52 週 新高、近 20 個交易日平均成交金額 ≥ 50 百萬元(只限新買進);大盤濾網:換股日加權指數低於 60 日均線就空手;市場廣度濾網:換股日站上 60 日均線的股票比例低於 40% 就空手;續抱條件:上一期持股 RS ≥ 50 就續抱;產業上限:同產業最多 4 檔;換股緩衝:上一期持股仍在前 40 名就續抱;換股日偏移:起日之後先跳過 3 個交易日。",
		"\n目前持股(2026-10-14 換股、已過 3 個交易日,再 17 個交易日換股;還沒滿持有天數,不計入上面的績效):持有 2 檔(相對上一期換手 25%):2330 台積電、79979。",
		"勝過基準 50%。因大盤濾網空手 1 期。",
		"共 8 期(有持股 7 期,平均 20 檔):累積報酬 35.2%(基準 12.6%)、年化 10.6%(基準 4%)、最大回撤 -18.3%(基準 -9.1%)",
		"每期平均 1.4%(基準 1.5%)、勝率 62.5%、勝過基準 50%。",
		"最近 6 期:",
		"\n- 2026-07-02 空手(大盤濾網,基準 1.5%)",
		"\n- 2026-06-02 持有 20 檔,報酬 3%(基準 1.5%);2330、2454",
		"\n- 2026-08-02 持有 20 檔,報酬 5%(扣成本 4.78%、換手 50%,基準 1.5%、0050 2.1%);2330、2454",
		"對照實際買得到的 0050(同樣每 20 個交易日進出、未扣成本):累積報酬 20.5%、年化 6.4%、最大回撤 -12%;勝過 0050 的期數 75%。",
		"依換股日年度(各年從頭起算,跨年的一期算在換股那年):",
		"風險:年化波動率 24.5%(基準 18%)、夏普值 1.12(扣成本後,無風險利率以 0 計;基準 0.95)。",
		"0050 年化波動率 17.2%、夏普值 1.31。",
		"\n- 2025 年 3 期:報酬 12.3%(扣成本 10.1%;基準 4.5%)、勝率 66.67%、勝過基準 100%",
		"\n- 2026 年 5 期:報酬 -3.2%(扣成本 -5%;基準 1.1%、0050 8.8%)、勝率 40%、勝過基準 20%",
		"扣交易成本(手續費 0.1425%×0.28 折扣、賣出證交稅 0.3%,依換手計算)後:累積報酬 30.1%、年化 9.2%、最大回撤 -19%;平均換手率 43.21%。",
		"期間內下市的股票算到最後一筆收盤",
		"個股貢獻(整段持有過 37 檔;",
		"\n- 貢獻最多:2330 台積電 +8.50 百分點(持有 12 期、平均 14.2%)、79979 +0.30 百分點(持有 1 期、平均 無)",
		"\n- 拖累最多:2454 聯發科 -1.25 百分點(持有 3 期、平均 -8.33%)",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	if strings.Contains(summary, "2026-02-02") || strings.Contains(summary, "存活者偏差") || strings.Contains(summary, "0050 核心部位") {
		t.Errorf("只列最近 6 期,不再提存活者偏差,etf_weight=0 不提核心部位:%s", summary)
	}
	e.RankBy, e.EtfWeight = ptr("trade_value"), ptr(30)
	ranked := screenBacktestSummary(e)
	for _, want := range []string{
		"依近 20 個交易日平均成交金額由高到低取前 20 檔等權持有",
		";換股日偏移:起日之後先跳過 3 個交易日;0050 核心部位 30%(其餘 70% 跑策略,報酬皆為混合後的組合)。",
	} {
		if !strings.Contains(ranked, want) {
			t.Errorf("摘要缺少 %q:%s", want, ranked)
		}
	}
	e.RankBy, e.EtfWeight = nil, nil
	e.Summary.TotalReturnAfterCosts, e.Summary.EtfTotalReturn, e.Years, e.Contributors, e.Current = nil, nil, nil, nil, nil
	e.RebalanceOffset = ptr(0)
	if old := screenBacktestSummary(e); strings.Contains(old, "扣交易成本") || strings.Contains(old, "對照實際買得到") || strings.Contains(old, "依換股日年度") || strings.Contains(old, "個股貢獻") ||
		strings.Contains(old, "目前持股") || strings.Contains(old, "換股日偏移") {
		t.Errorf("舊版 API 沒有成本、0050 與年度欄位時不列這些段落:%s", old)
	}

	empty := screenBacktestSummary(&ScreenBacktestEnvelope{From: "2026-10-01", To: "2026-10-14", Market: "twse", HoldDays: 240, Limit: 5, Adjusted: true})
	if !strings.Contains(empty, "期間內沒有已滿持有天數的期數") || strings.Contains(empty, "條件 ") {
		t.Errorf("沒有期數時的摘要錯誤:%s", empty)
	}
	// 沒有已滿的期數也列目前持股;大盤濾網空手與沒有符合條件分開說明。
	for current, want := range map[*BacktestCurrent]string{
		{Date: "2026-10-09", DaysElapsed: 3, DaysToNextRebalance: 237, MarketFiltered: true}: "已過 3 個交易日,再 237 個交易日換股;還沒滿持有天數,不計入上面的績效):大盤濾網沒過,空手。",
		{Date: "2026-10-09", DaysToNextRebalance: 240}:                                       "已過 0 個交易日,再 240 個交易日換股;還沒滿持有天數,不計入上面的績效):沒有符合條件的股票,空手。",
	} {
		got := screenBacktestSummary(&ScreenBacktestEnvelope{From: "2026-10-01", To: "2026-10-14", Market: "twse", HoldDays: 240, Limit: 5, Adjusted: true, Current: current})
		if !strings.Contains(got, "期間內沒有已滿持有天數的期數") || !strings.Contains(got, "\n目前持股(2026-10-09 換股、"+want) {
			t.Errorf("目前持股摘要缺少 %q:%s", want, got)
		}
	}
}

// TestBacktestSignalText 驗證訊號條件的說明:兩者、只有來源、只有種類、都沒有。
// TestRankByText 驗證各排序方式的中文說明;未指定與 rs_score 相同。
func TestRankByText(t *testing.T) {
	for rankBy, want := range map[string]string{
		"": "相對強度分數", "rs_score": "相對強度分數", "trade_value": "近 20 個交易日平均成交金額",
		"foreign_streak": "外資連續買超天數", "trust_streak": "投信連續買超天數",
	} {
		if got := rankByText(rankBy); got != want {
			t.Errorf("rankByText(%q) = %q,應為 %q", rankBy, got, want)
		}
	}
	if etfWeightText(0) != "" || etfWeightText(-1) != "" || !strings.HasPrefix(etfWeightText(100), "0050 核心部位 100%(其餘 0%") {
		t.Error("etfWeightText 未設定應為空,100% 應說明其餘 0%")
	}
}

func TestBacktestSignalText(t *testing.T) {
	for _, c := range []struct {
		indicator, kind *string
		want            string
	}{
		{ptr("kd"), ptr("golden_cross"), "KD 黃金交叉"},
		{ptr("rs80"), nil, "來源為 相對強度 80 分 的訊號"},
		{nil, ptr("new_high"), "種類為 新高 的訊號"},
		{nil, nil, ""},
	} {
		if got := backtestSignalText(c.indicator, c.kind); got != c.want {
			t.Errorf("backtestSignalText = %q,應為 %q", got, c.want)
		}
	}
}

// TestScreenBacktestToolIsRegisteredReadOnly 經由 tools/list 驗證工具有註冊且為唯讀,並檢查 schema。
func TestScreenBacktestToolIsRegisteredReadOnly(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	AddTools(server, &fakeScreenBacktestQuerier{}, nil)
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
		if tool.Name == "get_screen_backtest" {
			found = true
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Error("get_screen_backtest 必須有 ReadOnlyHint=true")
			}
		}
	}
	if !found {
		t.Error("tools/list 缺少 get_screen_backtest")
	}
	schema := screenBacktestSchema()
	if len(schema.Properties["hold_days"].Enum) != 5 || *schema.Properties["limit"].Maximum != 100 || *schema.Properties["min_trust_streak"].Maximum != 60 {
		t.Error("hold_days 應有 5 個列舉、limit 上限 100、min_trust_streak 上限 60")
	}
}
