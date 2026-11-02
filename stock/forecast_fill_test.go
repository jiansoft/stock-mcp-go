package stock

import (
	"strings"
	"testing"
)

// TestForecastFillText 驗證預估每筆附歷年填息:自身紀錄優先、沒有時寫同組中位數、都沒有不附;並出現在摘要裡。
func TestForecastFillText(t *testing.T) {
	own := DividendForecastItem{FillHistory: &DividendFillSummary{Events: 4, Filled: 3, AverageTradingDaysToFill: ptr(12.5)}}
	if got := forecastFillText(own); got != ";近 5 年填息 3/4 次、平均 12.5 個交易日" {
		t.Errorf("自身紀錄:%q", got)
	}
	peer := DividendForecastItem{FillPeer: &DividendFillBucket{Label: "3～5%", MedianTradingDaysToFill: ptr(13.0)}}
	if got := forecastFillText(peer); got != ";同殖利率組(3～5%)填息中位數 13 個交易日" {
		t.Errorf("同組基準:%q", got)
	}
	if got := forecastFillText(DividendForecastItem{}); got != "" {
		t.Errorf("都沒有應為空:%q", got)
	}

	f := sampleForecast()
	f.Items[0].FillHistory = own.FillHistory
	if summary := dividendForecastText(f); !strings.Contains(summary, ";近 5 年填息 3/4 次、平均 12.5 個交易日") {
		t.Fatalf("摘要缺少歷年填息:%s", summary)
	}
}
