package stock

// 本檔案測試多組回測摘要對回撤口徑與跨界排除期數的揭露。

import (
	"strings"
	"testing"
)

// TestOffsetsSummaryDisclosesBasisAndExcludedPeriods 驗證偏移摘要標示期末口徑,並在有分割日時列出排除的跨界期數。
func TestOffsetsSummaryDisclosesBasisAndExcludedPeriods(t *testing.T) {
	e := sampleBacktestOffsets()
	e.DrawdownBasis = ptr("period_end")
	split := "2024-01-01"
	e.Split = &split
	e.Offsets = []BacktestOffsetResult{{ExcludedCrossBoundaryPeriods: ptr(1)}, {ExcludedCrossBoundaryPeriods: ptr(2)}, {}}
	text := screenBacktestOffsetsSummary(e, ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	for _, want := range []string{"最大回撤(換股期末口徑)", "各偏移合計 3 期"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少「%s」:%s", want, text)
		}
	}
	e.Split = nil
	e.DrawdownBasis = nil
	text = screenBacktestOffsetsSummary(e, ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	if strings.Contains(text, "換股期末口徑") || strings.Contains(text, "各偏移合計") {
		t.Fatalf("沒有口徑或分割日時不應揭露:%s", text)
	}
	if excludedCrossBoundary(nil) != 0 {
		t.Fatal("空結果應為 0")
	}
}

// TestWalkForwardSummaryDisclosesExcludedTrainingPeriods 驗證逐年說明會標出訓練排除的跨年期數。
func TestWalkForwardSummaryDisclosesExcludedTrainingPeriods(t *testing.T) {
	e := &ScreenBacktestWalkForwardEnvelope{TrainYears: 3, Years: []BacktestWalkForwardYear{
		{Year: 2024, ExcludedTrainingPeriods: ptr(1), Periods: 12},
		{Year: 2025, ExcludedTrainingPeriods: ptr(0), Periods: 12},
	}}
	text := screenBacktestWalkForwardSummary(e, ScreenBacktestOptions{HoldDays: 20, Limit: 20})
	if !strings.Contains(text, "訓練排除 1 期跨年持有") {
		t.Fatalf("應揭露排除期數:%s", text)
	}
	if strings.Count(text, "訓練排除") != 1 {
		t.Fatalf("0 期不應揭露:%s", text)
	}
}
