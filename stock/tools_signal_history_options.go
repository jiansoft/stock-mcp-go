// get_signal_performance／get_stock_signal_history 的參數驗證：期間長度、訊號篩選、分組與相對強度／法人連買門檻。

package stock

import (
	"fmt"
	"strings"
	"time"
)

// maxPerformancePeriodDays 是訊號績效與法人連買報酬統計期間的上限(日曆天,含起訖),與 Data API 一致;
// 技術訊號、報酬、相對強度與籌碼都從 2019 年起。
const maxPerformancePeriodDays = 3653

// maxAllSignalsPeriodDays 是訊號績效不指定 indicator 時的期間上限:全部來源一次統計多年太慢。
const maxAllSignalsPeriodDays = 1096

// normalizeGroupBy 整理 group_by:空字串為不拆分,只接受 year。
func normalizeGroupBy(raw string) (string, error) {
	groupBy := strings.ToLower(strings.TrimSpace(raw))
	if groupBy != "" && groupBy != "year" {
		return "", fmt.Errorf("參數 group_by 只支援 year,收到了 %q", raw)
	}
	return groupBy, nil
}

// validateStreakBounds 檢查連續買超天數下限:0 代表不篩選,其餘需在 1 至 60。
func validateStreakBounds(foreign, trust int) error {
	for _, bound := range []struct {
		name  string
		value int
	}{{"min_foreign_streak", foreign}, {"min_trust_streak", trust}} {
		if bound.value != 0 && (bound.value < 1 || bound.value > maxStreakDays) {
			return fmt.Errorf("參數 %s 必須介於 1 到 %d 之間", bound.name, maxStreakDays)
		}
	}
	return nil
}

// validateRSScoreBounds 檢查相對強度分數上下限:0 代表不篩選,其餘需在 1 至 99,且下限不可大於上限。
func validateRSScoreBounds(minScore, maxScore int) error {
	for _, bound := range []struct {
		name  string
		value int
	}{{"min_rs_score", minScore}, {"max_rs_score", maxScore}} {
		if bound.value != 0 && (bound.value < 1 || bound.value > 99) {
			return fmt.Errorf("參數 %s 必須介於 1 到 99 之間", bound.name)
		}
	}
	if minScore > 0 && maxScore > 0 && minScore > maxScore {
		return fmt.Errorf("參數 min_rs_score 不可大於 max_rs_score")
	}
	return nil
}

// normalizeSignalFilters 去除前後空白並驗證 kind 與 indicator。
func normalizeSignalFilters(rawKind, rawIndicator string) (string, string, error) {
	kind := strings.TrimSpace(rawKind)
	if kind != "" && signalKindLabel(kind) == "" {
		return "", "", fmt.Errorf("參數 kind 不是可用的訊號種類:%q", rawKind)
	}
	indicator := strings.TrimSpace(rawIndicator)
	if indicator != "" && signalIndicatorLabel(indicator) == "" {
		return "", "", fmt.Errorf("參數 indicator 不是可用的訊號來源:%q", rawIndicator)
	}
	return kind, indicator, nil
}

// validatePerformancePeriod 在起訖都有指定時檢查先後與長度(含起訖不超過 3,653 天)。
func validatePerformancePeriod(from, to string) error {
	if from == "" || to == "" {
		return nil
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return fmt.Errorf("參數 from 格式錯誤")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return fmt.Errorf("參數 to 格式錯誤")
	}
	if start.After(end) {
		return fmt.Errorf("參數 from 不可晚於 to")
	}
	if int(end.Sub(start).Hours()/24) >= maxPerformancePeriodDays {
		return fmt.Errorf("統計期間最長 %d 天", maxPerformancePeriodDays)
	}
	return nil
}

// requireIndicatorForLongPeriod 在沒有指定 indicator 時,檢查起日到迄日(未指定迄日用 now 的台北日期)
// 不超過 1,096 天;Data API 也會拒絕,但它的 422 會被當成產業不存在,所以先在工具層擋下。
func requireIndicatorForLongPeriod(from, to, indicator string, now time.Time) error {
	if indicator != "" || from == "" {
		return nil
	}
	if to == "" {
		to = now.In(taipeiLocation).Format("2006-01-02")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return fmt.Errorf("參數 from 格式錯誤")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return fmt.Errorf("參數 to 格式錯誤")
	}
	if int(end.Sub(start).Hours()/24) >= maxAllSignalsPeriodDays {
		return fmt.Errorf("統計期間超過 1,096 天時必須指定 indicator(訊號來源),例如 52_week")
	}
	return nil
}
