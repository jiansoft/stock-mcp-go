// Package stock 測試回測工具的樣本內外分割(split)與排除產業(exclude_industry)。
package stock

import (
	"strings"
	"testing"
)

// TestValidateBacktestSplit 驗證分割日的格式與起訖範圍;沒有起訖時只檢查格式。
func TestValidateBacktestSplit(t *testing.T) {
	if got, err := validateBacktestSplit("", "2019-01-02", "2026-10-15"); err != nil || got != "" {
		t.Fatalf("空字串為不分割:%q %v", got, err)
	}
	if got, err := validateBacktestSplit("2023-01-02", "2019-01-02", "2026-10-15"); err != nil || got != "2023-01-02" {
		t.Fatalf("合法分割日:%q %v", got, err)
	}
	if _, err := validateBacktestSplit("2023-01-02", "", ""); err != nil {
		t.Fatalf("沒有起訖時只檢查格式:%v", err)
	}
	for _, c := range []struct{ split, from, to, want string }{
		{"2023/01/02", "", "", "格式錯誤"},
		{"2019-01-02", "2019-01-02", "", "必須晚於 from"},
		{"2026-10-16", "", "2026-10-15", "不可晚於 to"},
	} {
		if _, err := validateBacktestSplit(c.split, c.from, c.to); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v 應回 %q:%v", c, c.want, err)
		}
	}
}

// TestScreenBacktestSplitAndExcludeOptions 驗證參數整理、query string 與單次回測不送 split。
func TestScreenBacktestSplitAndExcludeOptions(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{
		From: "2019-01-02", To: "2026-10-15", ExcludeIndustry: " 金融保險業,航運業 ", Split: " 2023-01-02 ",
	})
	if err != nil {
		t.Fatalf("合法參數不應失敗:%v", err)
	}
	if opt.ExcludeIndustry != "金融保險業,航運業" || opt.Split != "2023-01-02" {
		t.Fatalf("參數整理錯誤:%#v", opt)
	}
	values := opt.query()
	if values.Get("exclude_industry") != "金融保險業,航運業" || values.Get("split") != "2023-01-02" {
		t.Fatalf("query string 錯誤:%s", values.Encode())
	}
	if _, err := screenBacktestOptions(ScreenBacktestInput{ExcludeIndustry: strings.Repeat("產", 201)}); err == nil {
		t.Error("exclude_industry 過長應回錯誤")
	}
	if _, err := screenBacktestOptions(ScreenBacktestInput{From: "2019-01-02", Split: "2018-01-02"}); err == nil {
		t.Error("split 早於 from 應回錯誤")
	}

	f := &fakeScreenBacktestQuerier{envelope: sampleScreenBacktest()}
	ts := &screenBacktestToolset{bq: f, logf: func(string, ...any) {}}
	if _, _, err := ts.screenBacktest(t.Context(), nil, ScreenBacktestInput{Split: "2023-01-02", ExcludeIndustry: "航運業"}); err != nil {
		t.Fatalf("screenBacktest 不應失敗:%v", err)
	}
	if f.got.Split != "" || f.got.ExcludeIndustry != "航運業" {
		t.Fatalf("單次回測不送 split、要送 exclude_industry:%#v", f.got)
	}

	if _, ok := screenBacktestSchema().Properties["split"]; ok {
		t.Error("get_screen_backtest 不收 split")
	}
	for name, schema := range map[string]map[string]bool{
		"offsets":  {"split": true, "exclude_industry": true},
		"frontier": {"split": true, "exclude_industry": true},
	} {
		properties := screenBacktestOffsetsSchema().Properties
		if name == "frontier" {
			properties = screenBacktestFrontierSchema().Properties
		}
		for field := range schema {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s schema 缺少 %s", name, field)
			}
		}
	}
}

// TestBacktestToolErrorNamesIndustries 驗證 422 的訊息會列出 industry 與 exclude_industry,都沒有時改說明條件被拒。
func TestBacktestToolErrorNamesIndustries(t *testing.T) {
	logf := func(string, ...any) {}
	err := backtestToolError(ErrIndustryNotFound, "get_screen_backtest", ScreenBacktestOptions{Industry: "半導體業", ExcludeIndustry: "不存在, 航運業"}, logf)
	if err == nil || !strings.Contains(err.Error(), "找不到產業「半導體業、不存在、航運業」") {
		t.Fatalf("應列出全部產業:%v", err)
	}
	err = backtestToolError(ErrIndustryNotFound, "get_screen_backtest_offsets", ScreenBacktestOptions{Split: "2026-10-15"}, logf)
	if err == nil || !strings.Contains(err.Error(), "拒絕了這組條件") {
		t.Fatalf("沒有產業時應說明條件被拒:%v", err)
	}
}

// TestBacktestSplitSummaries 驗證單次回測列出排除產業、穩健度列出樣本內外、前緣列出樣本外與樣本內最佳比例。
func TestBacktestSplitSummaries(t *testing.T) {
	e := sampleScreenBacktest()
	e.ExcludeIndustry = ptr("金融保險業")
	if summary := screenBacktestSummary(e); !strings.Contains(summary, "排除產業「金融保險業」") {
		t.Errorf("單次回測摘要缺少排除產業:%s", summary)
	}

	offsets := sampleBacktestOffsets()
	offsets.Split = ptr("2023-01-02")
	offsets.BeforeSplit = &BacktestOffsetDistribution{
		AnnualizedReturnAfterCosts: &BacktestStat{Median: 25.5}, SharpeRatio: &BacktestStat{Median: 1.2},
		MaxDrawdownAfterCosts: &BacktestStat{Median: -20}, EtfAnnualizedReturn: &BacktestStat{Median: 18},
		BeatEtfReturnRate: ptr(0.9), BeatEtfSharpeRate: ptr(0.8),
	}
	text := screenBacktestOffsetsSummary(offsets, ScreenBacktestOptions{HoldDays: 20, Limit: 20, ExcludeIndustry: "金融保險業"})
	for _, want := range []string{
		"排除產業「金融保險業」",
		"\n樣本內(換股日早於 2023-01-02):年化 25.5%、夏普 1.2、最大回撤 -20%(中位數),同期 0050 年化 18%;年化報酬勝過 0050 的偏移比例 90%、夏普 80%",
		"\n樣本外(換股日從 2023-01-02 起):無資料",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("穩健度摘要缺少 %q:%s", want, text)
		}
	}

	frontier := sampleBacktestFrontier()
	frontier.Split = ptr("2023-01-02")
	frontier.BestSharpeWeightBeforeSplit = ptr(40)
	frontier.Weights[0].AfterSplit = &BacktestOffsetDistribution{AnnualizedReturnAfterCosts: &BacktestStat{Median: 31}, SharpeRatio: &BacktestStat{Median: 1.4}}
	text = screenBacktestFrontierSummary(frontier, ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	for _, want := range []string{
		";樣本外 年化 31%、夏普 1.4",
		"\n樣本內外以 2023-01-02 分割(換股日早於這天為樣本內):只看樣本內時夏普值中位數最高的比例 40%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("前緣摘要缺少 %q:%s", want, text)
		}
	}
}

// TestBacktestCurrentReturnToDate 驗證目前持股列出各檔與策略部位本期至今報酬、0050 同期;舊版 API 沒有時不列。
func TestBacktestCurrentReturnToDate(t *testing.T) {
	e := sampleScreenBacktest()
	e.Current = &BacktestCurrent{
		Date: "2026-10-01", DaysElapsed: 10, DaysToNextRebalance: 10, Turnover: 0.1,
		Holdings: []BacktestHolding{
			{Symbol: "2330", Name: ptr("台積電"), Weight: ptr(50.0), Added: ptr(false), ReturnToDate: ptr(5.234)},
			{Symbol: "2317", Name: ptr("鴻海"), Weight: ptr(50.0), Added: ptr(true)},
		},
		ReturnToDate: ptr(2.6), EtfReturnToDate: ptr(-1.0),
	}
	summary := screenBacktestSummary(e)
	for _, want := range []string{
		"新買進 2317 鴻海;續抱 2330 台積電 +5.23%。",
		"本期至今(換股日收盤到迄日,依權重、未扣成本)+2.60%,0050 同期 -1.00%。",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("摘要缺少 %q:%s", want, summary)
		}
	}
	e.Current.EtfReturnToDate = nil
	if text := screenBacktestSummary(e); !strings.Contains(text, "未扣成本)+2.60%。") {
		t.Errorf("沒有 0050 時只列策略:%s", text)
	}
	e.Current.ReturnToDate = nil
	if text := screenBacktestSummary(e); strings.Contains(text, "本期至今") {
		t.Errorf("舊版 API 不列本期至今:%s", text)
	}
}
