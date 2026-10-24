package stock

import (
	"strings"
	"testing"
)

// TestEtfProxyTrackingSummary 驗證樣本不足時明說原因並列出資料日，有值時單位完整。
func TestEtfProxyTrackingSummary(t *testing.T) {
	asOf := "2026-10-23"
	empty := &EtfProxyTracking{BenchmarkSymbol: "0050", Samples: 0, Note: "備註"}
	text := etfProxyTrackingSummary("00631L", empty)
	if !strings.Contains(text, "尚無足夠樣本") || !strings.Contains(text, "資料日 無") || strings.Contains(text, "無 個百分點") {
		t.Fatalf("樣本不足的摘要不符:%s", text)
	}
	tracking, difference := 4.2, -3.5
	full := &EtfProxyTracking{BenchmarkSymbol: "0050", DataAsOf: &asOf, Samples: 240, TrackingErrorPercent: &tracking, TrackingDifferencePercent: &difference, Note: "備註"}
	text = etfProxyTrackingSummary("0056", full)
	for _, want := range []string{"資料日 2026-10-23", "有效樣本 240", "年化報酬差波動 4.2%", "累積報酬差 -3.5 個百分點"} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:%s", want, text)
		}
	}
	partial := &EtfProxyTracking{BenchmarkSymbol: "0050", DataAsOf: &asOf, Samples: 200, TrackingErrorPercent: &tracking, Note: "備註"}
	if text = etfProxyTrackingSummary("0056", partial); !strings.Contains(text, "累積報酬差 無。") {
		t.Fatalf("部分缺值的摘要不符:%s", text)
	}
}
