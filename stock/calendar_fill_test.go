package stock

import (
	"strings"
	"testing"
)

// TestCalendarFillText 驗證有自身紀錄寫次數與平均、沒有時寫同組中位數、都沒有的不列,最多列 calendarFillListed 筆。
func TestCalendarFillText(t *testing.T) {
	events := []DividendEvent{
		{StockSymbol: "2330", Name: "台積電", EventType: "ex_dividend", EventDate: "2026-12-11",
			FillHistory: &DividendFillSummary{Events: 4, Filled: 4, AverageTradingDaysToFill: ptr(2.5)}},
		{StockSymbol: "1101", Name: "台泥", EventType: "ex_dividend", EventDate: "2026-12-12",
			FillPeer: &DividendFillBucket{Label: "3～5%", MedianTradingDaysToFill: ptr(12.0)}},
		{StockSymbol: "2330", Name: "台積電", EventType: "cash_payable", EventDate: "2027-01-08"},
		{StockSymbol: "2884", Name: "玉山金", EventType: "ex_rights", EventDate: "2026-12-13",
			FillHistory: &DividendFillSummary{Events: 2, Filled: 0}},
	}
	got := calendarFillText(events)
	for _, want := range []string{
		"除權息事件的歷年填息:2026-12-11 2330 台積電 近 5 年填息 4/4 次、平均 2.5 個交易日",
		";2026-12-12 1101 台泥 無自身紀錄,同殖利率組(3～5%)填息中位數 12 個交易日",
		";2026-12-13 2884 玉山金 近 5 年填息 0/2 次。",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:%s", want, got)
		}
	}
	if strings.Contains(got, "2027-01-08") {
		t.Errorf("發放日事件不應列出:%s", got)
	}
	if calendarFillText([]DividendEvent{{EventType: "cash_payable"}}) != "" {
		t.Error("沒有可列的事件應回空字串")
	}
	many := make([]DividendEvent, calendarFillListed+3)
	for i := range many {
		many[i] = events[0]
	}
	if n := strings.Count(calendarFillText(many), "近 5 年填息"); n != calendarFillListed {
		t.Errorf("最多列 %d 筆,實際 %d", calendarFillListed, n)
	}
}
