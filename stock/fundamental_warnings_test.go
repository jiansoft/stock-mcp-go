// Package stock 測試基本面相關的新增項目:回測依營收年增率與營業利益率排序、持股基本面警示、
// 時間軸的營收新高與三率事件。
package stock

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBacktestRankByFundamentals 驗證 rank_by 可用 revenue_yoy、operating_margin,並帶入 query string 與摘要。
func TestBacktestRankByFundamentals(t *testing.T) {
	for rankBy, want := range map[string]string{
		"revenue_yoy":      "最新月營收年增率",
		"operating_margin": "最近一季營業利益率",
	} {
		opt, err := screenBacktestOptions(ScreenBacktestInput{RankBy: rankBy})
		if err != nil {
			t.Fatalf("rank_by=%s 不應失敗:%v", rankBy, err)
		}
		if got := opt.query().Get("rank_by"); got != rankBy {
			t.Errorf("query rank_by = %q,應為 %q", got, rankBy)
		}
		if got := rankByText(rankBy); got != want {
			t.Errorf("rankByText(%q) = %q,應為 %q", rankBy, got, want)
		}
	}
	if _, err := screenBacktestOptions(ScreenBacktestInput{RankBy: "eps"}); err == nil || !strings.Contains(err.Error(), "operating_margin") {
		t.Fatalf("不認得的 rank_by 應列出可用值:%v", err)
	}
	enum := screenBacktestSchema().Properties["rank_by"].Enum
	if len(enum) != len(backtestRankBy) || enum[4] != "revenue_yoy" || enum[5] != "operating_margin" {
		t.Fatalf("rank_by enum 錯誤:%v", enum)
	}
}

// warnedHolding 是有三種警示的持股:營收連 3 月年減、三率三降、營業虧損。
func warnedHolding() PortfolioHolding {
	return PortfolioHolding{
		StockSymbol: "2330", Name: "台積電", Shares: 1000,
		RevenueMonth: ptr("2026-09"), RevenueYoYPercent: ptr(-12.5), RevenueDeclineMonths: ptr(3),
		MarginPeriod: ptr("2026-Q2"), GrossMarginPercent: ptr(40.13), OperatingMarginPercent: ptr(-1.5), NetMarginPercent: ptr(2.0),
		MarginsTrend: ptr("all_down"), Warnings: []string{"revenue_decline", "margins_all_down", "operating_loss"},
	}
}

// TestPortfolioSummaryWarnings 驗證回應欄位解析,以及摘要列出有警示的持股(不受持股檔數上限影響)。
func TestPortfolioSummaryWarnings(t *testing.T) {
	var holding PortfolioHolding
	body := `{"stock_symbol":"2330","name":"台積電","shares":1000,"revenue_month":"2026-09","revenue_yoy_percent":-12.5,` +
		`"revenue_decline_months":3,"margin_period":"2026-Q2","gross_margin_percent":40.13,"operating_margin_percent":-1.5,` +
		`"net_margin_percent":2,"margins_trend":"all_down","warnings":["revenue_decline","margins_all_down","operating_loss"]}`
	if err := json.Unmarshal([]byte(body), &holding); err != nil {
		t.Fatalf("解析持股失敗:%v", err)
	}
	want := warnedHolding()
	if *holding.RevenueDeclineMonths != 3 || *holding.MarginsTrend != "all_down" || len(holding.Warnings) != 3 ||
		*holding.OperatingMarginPercent != *want.OperatingMarginPercent || *holding.MarginPeriod != "2026-Q2" {
		t.Fatalf("持股欄位解析錯誤:%#v", holding)
	}

	summary := samplePortfolio()
	summary.WarningSymbols = 1
	// 有警示的持股排在第 12 檔,超過摘要列出的 10 檔,仍要出現在警示段。
	for i := 0; i < 10; i++ {
		summary.Holdings = append(summary.Holdings, PortfolioHolding{StockSymbol: "1101", Name: "台泥"})
	}
	summary.Holdings = append(summary.Holdings, warnedHolding())
	text := portfolioSummaryText(summary)
	for _, wantText := range []string{
		"基本面警示 1 檔:",
		"- 2330 台積電:營收連續年減、三率三降、營業虧損;2026-09 營收年增 -12.5%(連續 3 個月年減);" +
			"2026-Q2 毛利率 40.13%、營益率 -1.5%、淨利率 2%(三率三降)",
	} {
		if !strings.Contains(text, wantText) {
			t.Errorf("摘要缺少 %q:%s", wantText, text)
		}
	}
	if !strings.Contains(portfolioSummaryText(samplePortfolio()), "基本面警示:無。") {
		t.Error("沒有警示時應寫明「無」")
	}
}

// TestFundamentalsText 涵蓋沒有營收與財報、最新一個月沒有年減、三率有升有降與不認得的警示代碼。
func TestFundamentalsText(t *testing.T) {
	if got := fundamentalsText(PortfolioHolding{}); got != "查無營收與財報" {
		t.Errorf("fundamentalsText(空) = %q", got)
	}
	mixed := PortfolioHolding{
		RevenueMonth: ptr("2026-09"), RevenueYoYPercent: ptr(8.0), RevenueDeclineMonths: ptr(0),
		MarginPeriod: ptr("2026-Q2"), GrossMarginPercent: ptr(30.0), MarginsTrend: ptr("mixed"),
	}
	if got := fundamentalsText(mixed); got != "2026-09 營收年增 8%;2026-Q2 毛利率 30%、營益率 無、淨利率 無(三率有升有降)" {
		t.Errorf("fundamentalsText(mixed) = %q", got)
	}
	unknown := PortfolioHolding{StockSymbol: "9999", Name: "測試", Warnings: []string{"new_warning"}}
	summary := &PortfolioSummary{Symbols: 1, WarningSymbols: 1, Holdings: []PortfolioHolding{unknown}}
	if text := portfolioSummaryText(summary); !strings.Contains(text, "- 9999 測試:new_warning;查無營收與財報") {
		t.Errorf("不認得的警示代碼應原樣列出:%s", text)
	}
}

// TestTimelineFundamentalKinds 驗證時間軸接受營收新高與三率事件,並有中文名稱。
func TestTimelineFundamentalKinds(t *testing.T) {
	f := &fakeTimelineQuerier{envelope: sampleTimeline()}
	ts := &stockTimelineToolset{tq: f, logf: func(string, ...any) {}}
	if _, _, err := ts.stockTimeline(t.Context(), nil, StockTimelineInput{Symbol: "2330", Kinds: []string{"revenue_record", "margin_trend"}}); err != nil {
		t.Fatalf("新種類應可篩選:%v", err)
	}
	if strings.Join(f.got.Kinds, ",") != "revenue_record,margin_trend" {
		t.Fatalf("種類未傳給 API:%v", f.got.Kinds)
	}
	if timelineKindLabels["revenue_record"] != "營收新高" || timelineKindLabels["margin_trend"] != "三率三升/三降" {
		t.Fatalf("中文名稱錯誤:%v", timelineKindLabels)
	}
}
