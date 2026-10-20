package stock

import (
	"fmt"
	"math"

	"github.com/google/jsonschema-go/jsonschema"
)

// 回測的基本面條件:月營收與季報,只用換股當天已公布的資料(Data API 依法定公布期限判斷)。

// BacktestFundamentals 是回測的基本面門檻;nil、0(連續月數)與 false(三率三升、營收新高)代表不篩選。
type BacktestFundamentals struct {
	MinRevenueYoYPercent      *float64
	MinRevenueYoYStreak       int
	MinRevenueYoY3mPercent    *float64
	MinEPS                    *float64
	MinROEPercent             *float64
	MinGrossMarginPercent     *float64
	MinOperatingMarginPercent *float64
	MarginsYoYUp              bool
	MaxDebtRatioPercent       *float64
	RevenueRecord             bool
}

// backtestFundamentalSchemas 回傳基本面門檻參數的 schema,加進回測類工具的輸入。
func backtestFundamentalSchemas() map[string]*jsonschema.Schema {
	revenueRule := "月營收法定次月 10 日前公布,換股日只用已公布的月份(次月 11 日起),最近一筆距換股日超過 3 個月視為沒有資料;只用於新買進,續抱不看基本面"
	reportRule := "季報以法定期限隔天起才用(Q1 5/16、Q2 8/15、Q3 11/15、Q4 隔年 4/1),距換股日超過 3 季不用;財報從 2018 年起,為目前留存的數字;只用於新買進"
	statementRule := "用 Yahoo 單季財報(2015 年起),公布日規則同 min_eps(法定期限隔天起才用、距換股日超過 3 季不用);沒有資料的股票不通過;只用於新買進"
	return map[string]*jsonschema.Schema{
		"min_revenue_yoy_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一個月營收年增率至少這麼高(%,選填,-100 至 10000)。" + revenueRule,
		},
		"min_revenue_yoy_streak": {
			Type: "integer", Minimum: ptr(1.0), Maximum: ptr(12.0),
			Description: "換股當天已公布的月營收從最近一個月往回連續年增(>0、月份相連)至少這麼多個月(選填,1 至 12)。" + revenueRule,
		},
		"min_revenue_yoy_3m_percent": {
			Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近 3 個月(須相連)合計營收年增率至少這麼高(%,選填,-100 至 10000)。" + revenueRule,
		},
		"min_eps": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一季每股盈餘至少這麼高(元,選填,-10000 至 10000)。" + reportRule,
		},
		"min_roe_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(10000.0),
			Description: "換股當天已公布的最近一季股東權益報酬率至少這麼高(%,選填,-10000 至 10000)。" + reportRule,
		},
		"min_gross_margin_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(100.0),
			Description: "換股當天已公布的最近一季單季毛利率至少這麼高(%,選填,-10000 至 100)。" + statementRule,
		},
		"min_operating_margin_percent": {
			Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(100.0),
			Description: "換股當天已公布的最近一季單季營業利益率至少這麼高(%,選填,-10000 至 100)。" + statementRule,
		},
		"margins_yoy_up": {
			Type:        "boolean",
			Description: "true 時只選三率三升:換股當天已公布的最近一季毛利率、營業利益率、稅後淨利率都高於去年同一季(去年同季沒有資料的不通過)。" + statementRule,
		},
		"max_debt_ratio_percent": {
			Type: "number", Minimum: ptr(0.0), Maximum: ptr(1000.0),
			Description: "換股當天已公布的最近一季季末負債比不超過這個值(%,選填,0 至 1000)。" + statementRule,
		},
		"revenue_record": {
			Type:        "boolean",
			Description: "true 時只選換股當天已公布的最新月營收創歷史新高(高於之前所有月份,之前至少 12 個月)的股票。" + revenueRule,
		},
	}
}

// validate 檢查基本面門檻的範圍(與 Data API 相同)。
func (f BacktestFundamentals) validate() error {
	for _, bound := range []struct {
		name     string
		value    *float64
		min, max float64
	}{
		{"min_revenue_yoy_percent", f.MinRevenueYoYPercent, -100, 10000},
		{"min_revenue_yoy_3m_percent", f.MinRevenueYoY3mPercent, -100, 10000},
		{"min_eps", f.MinEPS, -10000, 10000},
		{"min_roe_percent", f.MinROEPercent, -10000, 10000},
		{"min_gross_margin_percent", f.MinGrossMarginPercent, -10000, 100},
		{"min_operating_margin_percent", f.MinOperatingMarginPercent, -10000, 100},
		{"max_debt_ratio_percent", f.MaxDebtRatioPercent, 0, 1000},
	} {
		if v := bound.value; v != nil && (math.IsNaN(*v) || *v < bound.min || *v > bound.max) {
			return fmt.Errorf("參數 %s 必須介於 %s 到 %s 之間", bound.name, formatFloat(bound.min), formatFloat(bound.max))
		}
	}
	if f.MinRevenueYoYStreak != 0 && (f.MinRevenueYoYStreak < 1 || f.MinRevenueYoYStreak > 12) {
		return fmt.Errorf("參數 min_revenue_yoy_streak 必須介於 1 到 12 之間")
	}
	return nil
}

// set 把有設定的門檻加進 query string。
func (f BacktestFundamentals) set(values interface{ Set(key, value string) }) {
	for key, value := range map[string]*float64{
		"min_revenue_yoy_percent": f.MinRevenueYoYPercent, "min_revenue_yoy_3m_percent": f.MinRevenueYoY3mPercent,
		"min_eps": f.MinEPS, "min_roe_percent": f.MinROEPercent,
		"min_gross_margin_percent": f.MinGrossMarginPercent, "min_operating_margin_percent": f.MinOperatingMarginPercent,
		"max_debt_ratio_percent": f.MaxDebtRatioPercent,
	} {
		if value != nil {
			values.Set(key, formatFloat(*value))
		}
	}
	if f.MinRevenueYoYStreak > 0 {
		values.Set("min_revenue_yoy_streak", fmt.Sprint(f.MinRevenueYoYStreak))
	}
	if f.MarginsYoYUp {
		values.Set("margins_yoy_up", "true")
	}
	if f.RevenueRecord {
		values.Set("revenue_record", "true")
	}
}

// revenueRecordTexts 描述營收創歷史新高條件;false 代表不篩選。
func revenueRecordTexts(record bool) []string {
	if !record {
		return nil
	}
	return []string{"已公布的最新月營收創歷史新高"}
}

// fundamentalFilterTexts 描述有設定的基本面門檻;連續月數為 nil 或 0 代表不篩選。
func fundamentalFilterTexts(revenueYoY *float64, streak *int, revenue3m, eps, roe *float64) []string {
	var filters []string
	if revenueYoY != nil {
		filters = append(filters, fmt.Sprintf("已公布的最近月營收年增 ≥ %s%%", formatFloat(*revenueYoY)))
	}
	if streak != nil && *streak > 0 {
		filters = append(filters, fmt.Sprintf("月營收連續年增 ≥ %d 個月", *streak))
	}
	if revenue3m != nil {
		filters = append(filters, fmt.Sprintf("近 3 個月營收年增 ≥ %s%%", formatFloat(*revenue3m)))
	}
	if eps != nil {
		filters = append(filters, fmt.Sprintf("已公布的最近一季 EPS ≥ %s 元", formatFloat(*eps)))
	}
	if roe != nil {
		filters = append(filters, fmt.Sprintf("最近一季 ROE ≥ %s%%", formatFloat(*roe)))
	}
	return filters
}

// marginFilterTexts 描述有設定的財報三率與負債比門檻;三率三升為 false 代表不篩選。
func marginFilterTexts(gross, operating *float64, yoyUp bool, debt *float64) []string {
	var filters []string
	if gross != nil {
		filters = append(filters, fmt.Sprintf("最近一季毛利率 ≥ %s%%", formatFloat(*gross)))
	}
	if operating != nil {
		filters = append(filters, fmt.Sprintf("最近一季營業利益率 ≥ %s%%", formatFloat(*operating)))
	}
	if yoyUp {
		filters = append(filters, "三率三升(毛利率、營業利益率、淨利率都高於去年同季)")
	}
	if debt != nil {
		filters = append(filters, fmt.Sprintf("最近一季負債比 ≤ %s%%", formatFloat(*debt)))
	}
	return filters
}
