package stock

import (
	"strings"
	"testing"
)

// TestBacktestSummaryDisclosesMissingETF 確保缺值期數與停牌估算口徑對模型可見。
func TestBacktestSummaryDisclosesMissingETF(t *testing.T) {
	count := int64(2)
	summary := screenBacktestSummary(&ScreenBacktestEnvelope{Summary: BacktestSummary{EtfMissingPeriods: &count}})
	for _, want := range []string{"0050 缺值期數 2", "零報酬估算", "最多 30 天", "並非停牌日可實際成交"} {
		if !strings.Contains(summary, want) {
			t.Errorf("missing %q in %s", want, summary)
		}
	}
}
