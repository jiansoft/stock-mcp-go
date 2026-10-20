package stock

// 本檔案是 Phase 3:固定白名單條件選股的完整實作:輸入型別、JSON Schema、
// 驗證輔助函式、structuredContent 輸出型別與 handler。
//
// 這些工具由 tools.go 的 AddTools 依「資料來源是否具備對應能力介面」
// 動態註冊;共用的驗證與輸出輔助函式(normalizeSymbol、rangedLimit、
// textResult、displayFloat 等)也都定義在 tools.go。

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Phase 3:固定白名單條件選股
// ---------------------------------------------------------------------------

// screenToolset 綁定 Screener 與安全記錄函式。
type screenToolset struct {
	screener Screener
	logf     func(string, ...any)
}

// ScreenStocksInput 是 screen_stocks 工具的輸入。
// 浮點條件使用指標，讓 JSON 未提供欄位時是 nil，而明確提供 0 時仍保留
// 這個有效門檻；若使用 float64 值型別，兩種情況都會變成 Go 零值 0。
type ScreenStocksInput struct {
	Market                  string   `json:"market,omitempty"`
	IndustryID              int      `json:"industry_id,omitempty"`
	ValuationBand           string   `json:"valuation_band,omitempty"`
	MinRevenueYOYPercent    *float64 `json:"min_revenue_yoy_percent,omitempty"`
	MinRevenueYOYStreak     *int     `json:"min_revenue_yoy_streak,omitempty"`
	MinRevenueYOY3MPercent  *float64 `json:"min_revenue_yoy_3m_percent,omitempty"`
	MinMonthlyRevenue       *float64 `json:"min_monthly_revenue,omitempty"`
	MinEPS                  *float64 `json:"min_eps,omitempty"`
	MinROEPercent           *float64 `json:"min_roe_percent,omitempty"`
	MinDividendYieldPercent *float64 `json:"min_dividend_yield_percent,omitempty"`
	MinFScore               *int     `json:"min_f_score,omitempty"`
	MinGrossMarginPercent   *float64 `json:"min_gross_margin_percent,omitempty"`
	MaxDebtRatioPercent     *float64 `json:"max_debt_ratio_percent,omitempty"`
	// MinOperatingMarginPercent、MarginsYoYUp 取 Yahoo 最新一季單季損益表。
	// RevenueRecord 為 true 時只留最新月營收創歷史新高的股票。
	MinOperatingMarginPercent *float64 `json:"min_operating_margin_percent,omitempty"`
	MarginsYoYUp              bool     `json:"margins_yoy_up,omitempty"`
	RevenueRecord             bool     `json:"revenue_record,omitempty"`
	PositiveFreeCashFlow      bool     `json:"positive_free_cash_flow,omitempty"`
	MinRSScore                *int     `json:"min_rs_score,omitempty"`
	MinForeignStreak          *int     `json:"min_foreign_streak,omitempty"`
	MinTrustStreak            *int     `json:"min_trust_streak,omitempty"`
	MinAvgTradeValue          *int     `json:"min_avg_trade_value,omitempty"`
	SortBy                    string   `json:"sort_by,omitempty"`
	SortOrder                 string   `json:"sort_order,omitempty"`
	Limit                     int      `json:"limit,omitempty"`
}

// screenStocksSchema 只公開固定條件與排序 enum，拒絕任意欄位、運算式或
// SQL 片段。Schema 提供呼叫端第一層提示，handler 仍會再次驗證，因為
// 不可信 client 可能繞過 schema 直接送出 JSON-RPC 請求。
func screenStocksSchema() *jsonschema.Schema {
	number := func(minimum, maximum float64, desc string) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "number", Minimum: ptr(minimum), Maximum: ptr(maximum), Description: desc}
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"market":      listedOTCMarketSchema(),
			"industry_id": {Type: "integer", Minimum: ptr(1.0), Description: "產業代碼(正整數,選填)"},
			"valuation_band": {
				Type:        "string",
				Enum:        []any{"undervalued", "fair_valued", "overvalued", "highly_overvalued"},
				Description: "估值區間(選填)",
			},
			"min_revenue_yoy_percent": number(-100, 10000, "最低月營收年增率百分比"),
			"min_revenue_yoy_streak": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(12.0),
				Description: "最少連續月營收年增月數(1–12,從最新月份往回數、月份須相連)",
			},
			"min_revenue_yoy_3m_percent": number(-100, 10000, "最低近 3 個月累計月營收年增率百分比"),
			"min_monthly_revenue": number(0, 1e12,
				"最新一個月營收下限(新台幣千元,例如 10000 為 1,000 萬元);用來排除營收太小、年增率因低基期失真的股票"),
			"min_eps":                    number(-10000, 10000, "最低每股盈餘 EPS"),
			"min_roe_percent":            number(-10000, 10000, "最低股東權益報酬率百分比"),
			"min_dividend_yield_percent": number(0, 1000, "最低殖利率百分比"),
			"min_f_score": {
				Type: "integer", Minimum: ptr(0.0), Maximum: ptr(9.0),
				Description: "最低 Piotroski F 分數(0–9,年度財報 9 項指標)",
			},
			"min_gross_margin_percent":     number(-10000, 100, "最低最新一季毛利率百分比"),
			"max_debt_ratio_percent":       number(0, 1000, "最高最新一季季末負債比百分比"),
			"min_operating_margin_percent": number(-10000, 100, "最低最新一季單季營業利益率百分比(Yahoo 單季損益表)"),
			"margins_yoy_up": {
				Type:        "boolean",
				Description: "true 時只留三率三升:最新一季毛利率、營業利益率、稅後淨利率都高於去年同一季",
			},
			"revenue_record": {
				Type:        "boolean",
				Description: "true 時只留最新月營收創歷史新高(高於之前所有月份,之前至少 12 個月)的股票",
			},
			"positive_free_cash_flow": {
				Type:        "boolean",
				Description: "true 時只留近四季自由現金流為正的股票",
			},
			"min_rs_score": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(99.0),
				Description: "最低相對強度分數(1–99;近 60/120/240 日還原報酬加權後在上市櫃普通股(不含 ETF、特別股)的百分位,99 最強)",
			},
			"min_foreign_streak": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0),
				Description: "外資至少連續買超幾個交易日(1–60,截至最新籌碼資料日)",
			},
			"min_trust_streak": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0),
				Description: "投信至少連續買超幾個交易日(1–60,截至最新籌碼資料日)",
			},
			"min_avg_trade_value": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(100000.0),
				Description: "近 20 筆日線平均成交金額下限(百萬元,1–100000;例如 50 代表日均成交 5,000 萬元),與 get_screen_backtest 的同名參數一致,用來排除買不到量的股票",
			},
			"sort_by": {
				Type:        "string",
				Enum:        []any{"stock_symbol", "revenue_yoy", "revenue_yoy_streak", "revenue_yoy_3m", "monthly_revenue", "eps", "roe", "dividend_yield", "valuation_percentage", "f_score", "gross_margin", "debt_ratio", "operating_margin", "rs_score"},
				Default:     []byte(`"stock_symbol"`),
				Description: "固定白名單排序欄位",
			},
			"sort_order": {
				Type:        "string",
				Enum:        []any{"asc", "desc"},
				Default:     []byte(`"asc"`),
				Description: "排序方向",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("20"),
				Description: "最大回傳筆數(預設 20,範圍 1 至 50)",
			},
		},
	}
}

// ScreeningOutput 是 Data API envelope 加上 MCP 分析型共通欄位。
type ScreeningOutput struct {
	DataKind   string          `json:"data_kind"`
	IsRealtime bool            `json:"is_realtime"`
	Disclaimer string          `json:"disclaimer"`
	DataAsOf   *string         `json:"data_as_of"`
	RSAsOf     *string         `json:"rs_as_of"`
	ChipAsOf   *string         `json:"chip_as_of"`
	Stocks     []ScreenedStock `json:"stocks"`
}

// validateOptionalFloat 驗證選填數值的閉區間；nil 代表沒有提供，不檢查。
func validateOptionalFloat(field string, value *float64, minimum, maximum float64) error {
	// IEEE 754 的 NaN 與任何數比較都會是 false；若只寫上下界比較，NaN
	// 會同時通過「不小於最小值」與「不大於最大值」。Inf 雖會被一般
	// 上下界攔住，仍明確拒絕所有非有限數，讓驗證規則不依賴目前邊界。
	if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < minimum || *value > maximum) {
		return fmt.Errorf("參數 %s 必須介於 %s 到 %s 之間", field, formatFloat(minimum), formatFloat(maximum))
	}
	return nil
}

// screenStocks 執行固定條件選股：驗證白名單 → 呼叫 Data API → 組裝
// 繁中摘要與 structuredContent。
//
// MCP 的 protocol-level error（例如不存在的工具）由 SDK 處理；本 handler
// 回傳的 Go error 會成為 tool-level error。輸入錯誤可安全原樣告知使用者，
// 但 API 認證、timeout、5xx 或解析錯誤只能記錄在 server log，對 MCP
// 一律回通用訊息，避免內網位址、憑證狀態或實作細節外洩。
func (ts *screenToolset) screenStocks(ctx context.Context, _ *mcp.CallToolRequest, in ScreenStocksInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	if in.IndustryID < 0 {
		return nil, nil, fmt.Errorf("參數 industry_id 必須是正整數")
	}
	valuationBand := strings.ToLower(strings.TrimSpace(in.ValuationBand))
	if valuationBand != "" && valuationBand != "undervalued" && valuationBand != "fair_valued" && valuationBand != "overvalued" && valuationBand != "highly_overvalued" {
		return nil, nil, fmt.Errorf("參數 valuation_band 必須為 undervalued、fair_valued、overvalued 或 highly_overvalued")
	}
	for _, check := range []struct {
		field        string
		value        *float64
		minimum, max float64
	}{
		{"min_revenue_yoy_percent", in.MinRevenueYOYPercent, -100, 10000},
		{"min_revenue_yoy_3m_percent", in.MinRevenueYOY3MPercent, -100, 10000},
		{"min_monthly_revenue", in.MinMonthlyRevenue, 0, 1e12},
		{"min_eps", in.MinEPS, -10000, 10000},
		{"min_roe_percent", in.MinROEPercent, -10000, 10000},
		{"min_dividend_yield_percent", in.MinDividendYieldPercent, 0, 1000},
		{"min_gross_margin_percent", in.MinGrossMarginPercent, -10000, 100},
		{"max_debt_ratio_percent", in.MaxDebtRatioPercent, 0, 1000},
		{"min_operating_margin_percent", in.MinOperatingMarginPercent, -10000, 100},
	} {
		if err := validateOptionalFloat(check.field, check.value, check.minimum, check.max); err != nil {
			return nil, nil, err
		}
	}
	if in.MinRevenueYOYStreak != nil && (*in.MinRevenueYOYStreak < 1 || *in.MinRevenueYOYStreak > 12) {
		return nil, nil, fmt.Errorf("參數 min_revenue_yoy_streak 必須介於 1 到 12 之間")
	}
	if in.MinFScore != nil && (*in.MinFScore < 0 || *in.MinFScore > 9) {
		return nil, nil, fmt.Errorf("參數 min_f_score 必須介於 0 到 9 之間")
	}
	if in.MinRSScore != nil && (*in.MinRSScore < 1 || *in.MinRSScore > 99) {
		return nil, nil, fmt.Errorf("參數 min_rs_score 必須介於 1 到 99 之間")
	}
	if in.MinForeignStreak != nil && (*in.MinForeignStreak < 1 || *in.MinForeignStreak > 60) {
		return nil, nil, fmt.Errorf("參數 min_foreign_streak 必須介於 1 到 60 之間")
	}
	if in.MinTrustStreak != nil && (*in.MinTrustStreak < 1 || *in.MinTrustStreak > 60) {
		return nil, nil, fmt.Errorf("參數 min_trust_streak 必須介於 1 到 60 之間")
	}
	if in.MinAvgTradeValue != nil && (*in.MinAvgTradeValue < 1 || *in.MinAvgTradeValue > maxAvgTradeValueMillion) {
		return nil, nil, fmt.Errorf("參數 min_avg_trade_value 必須介於 1 到 100000(百萬元)之間")
	}

	// all 是預設資料範圍，不會縮小結果，單獨提供不算實質篩選；twse/tpex
	// 會把範圍縮到單一市場，所以本身即可作為必要的篩選條件。
	hasFilter := market != "all" || in.IndustryID > 0 || valuationBand != "" ||
		in.MinRevenueYOYPercent != nil || in.MinRevenueYOYStreak != nil || in.MinRevenueYOY3MPercent != nil || in.MinMonthlyRevenue != nil || in.MinEPS != nil || in.MinROEPercent != nil || in.MinDividendYieldPercent != nil ||
		in.MinFScore != nil || in.MinGrossMarginPercent != nil || in.MaxDebtRatioPercent != nil || in.PositiveFreeCashFlow ||
		in.MinOperatingMarginPercent != nil || in.MarginsYoYUp || in.RevenueRecord ||
		in.MinRSScore != nil || in.MinForeignStreak != nil || in.MinTrustStreak != nil || in.MinAvgTradeValue != nil
	if !hasFilter {
		return nil, nil, fmt.Errorf("至少需要一個實質篩選條件；market=all、排序與 limit 不算篩選條件")
	}

	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if sortBy == "" {
		sortBy = "stock_symbol"
	}
	validSort := map[string]bool{
		"stock_symbol": true, "revenue_yoy": true, "revenue_yoy_streak": true, "revenue_yoy_3m": true, "monthly_revenue": true, "eps": true, "roe": true, "dividend_yield": true, "valuation_percentage": true,
		"f_score": true, "gross_margin": true, "debt_ratio": true, "operating_margin": true, "rs_score": true,
	}
	if !validSort[sortBy] {
		return nil, nil, fmt.Errorf("參數 sort_by 不在允許的固定白名單內")
	}
	sortOrder := strings.ToLower(strings.TrimSpace(in.SortOrder))
	if sortOrder == "" {
		sortOrder = "asc"
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		return nil, nil, fmt.Errorf("參數 sort_order 必須為 asc 或 desc")
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 50)
	if err != nil {
		return nil, nil, err
	}

	envelope, err := ts.screener.ScreenStocks(ctx, ScreenOptions{
		Market: market, IndustryID: in.IndustryID, ValuationBand: valuationBand,
		MinRevenueYOYPercent: in.MinRevenueYOYPercent, MinRevenueYOYStreak: in.MinRevenueYOYStreak,
		MinRevenueYOY3MPercent: in.MinRevenueYOY3MPercent, MinMonthlyRevenue: in.MinMonthlyRevenue, MinEPS: in.MinEPS,
		MinROEPercent: in.MinROEPercent, MinDividendYieldPercent: in.MinDividendYieldPercent,
		MinFScore: in.MinFScore, MinGrossMarginPercent: in.MinGrossMarginPercent,
		MaxDebtRatioPercent: in.MaxDebtRatioPercent, PositiveFreeCashFlow: in.PositiveFreeCashFlow,
		MinOperatingMarginPercent: in.MinOperatingMarginPercent, MarginsYoYUp: in.MarginsYoYUp, RevenueRecord: in.RevenueRecord,
		MinRSScore: in.MinRSScore, MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		MinAvgTradeValue: in.MinAvgTradeValue, SortBy: sortBy, SortOrder: sortOrder, Limit: limit,
	})
	if err != nil {
		ts.logf("工具 screen_stocks 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定條件下沒有符合的股票。此工具只描述歷史資料符合情形,不替使用者做投資決策。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Stocks) > 0 {
		first := envelope.Stocks[0]
		summary = fmt.Sprintf("依固定條件篩選出 %d 檔股票；排序首筆為 %s %s（營收年增率 %s、連續年增 %s 個月、近 3 月累計年增 %s、最新月營收 %s（%s）、EPS %s、ROE %s、殖利率 %s、F 分數 %s（%s 年）、毛利率 %s、營業利益率 %s、淨利率 %s、%s、負債比 %s、近四季自由現金流 %s、相對強度 %s（%s）、外資%s、投信%s（%s）、近 20 日平均成交 %s；營收期 %s、財報期 %s、估值日 %s、殖利率日 %s、季報指標期 %s）。各股票指標日期可能不同,結果不構成買賣建議。\n免責聲明:%s",
			len(envelope.Stocks), first.StockSymbol, first.Name,
			displayPercent(first.RevenueYOYPercent), displayInt(first.RevenueYOYStreak), displayPercent(first.RevenueYOY3MPercent), thousandsToHundredMillion(first.MonthlyRevenue), revenueRecordText(first.RevenueRecord), displayFloat(first.EarningsPerShare), displayPercent(first.ReturnOnEquity), displayPercent(first.DividendYieldPercent),
			fScoreText(first.FScore, first.FScoreEvaluated), displayInt(first.FScoreYear),
			displayPercent(first.GrossMarginPercent), displayPercent(first.OperatingMarginPercent), displayPercent(first.NetMarginPercent),
			marginsYoYText(first.MarginsYoYUp, first.MarginPeriod), displayPercent(first.DebtRatioPercent), hundredMillion(first.FreeCashFlowTTM),
			displayInt(first.RSScore), displayString(envelope.RSAsOf),
			streakDisplay(first.ForeignStreak), streakDisplay(first.TrustStreak), displayString(envelope.ChipAsOf),
			millionText(first.AvgTradeValue20D),
			displayString(first.RevenueMonth), displayString(first.FinancialPeriod), displayString(first.ValuationDate), displayString(first.YieldDate),
			displayString(first.HealthPeriod), AnalysisDisclaimer)
	}
	return textResult(summary), ScreeningOutput{
		DataKind: "stock_screening_result", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, RSAsOf: envelope.RSAsOf, ChipAsOf: envelope.ChipAsOf, Stocks: envelope.Stocks,
	}, nil
}

// revenueRecordText 描述最新月營收是否創歷史新高;nil 回「是否新高無法判斷」。
func revenueRecordText(record *bool) string {
	switch {
	case record == nil:
		return "是否新高無法判斷"
	case *record:
		return "創歷史新高"
	default:
		return "未創新高"
	}
}

// marginsYoYText 描述三率三升判斷與損益表期間;nil 回「三率三升無法判斷」。
func marginsYoYText(up *bool, period *string) string {
	switch {
	case up == nil:
		return "三率三升無法判斷"
	case *up:
		return fmt.Sprintf("三率三升（%s）", displayString(period))
	default:
		return fmt.Sprintf("非三率三升（%s）", displayString(period))
	}
}

// streakDisplay 把連續買賣超天數轉成文字;nil 回「連續天數無資料」。
func streakDisplay(days *int) string {
	if days == nil {
		return "連續天數無資料"
	}
	return streakText(int64(*days))
}

// millionText 把百萬元金額顯示成「X 百萬元」;nil 回「無」。
func millionText(million *float64) string {
	if million == nil {
		return "無"
	}
	return displayFloat(million) + " 百萬元"
}

// thousandsToHundredMillion 把千元金額換算成「X 億元」;nil 回「無」。
func thousandsToHundredMillion(thousands *float64) string {
	if thousands == nil {
		return "無"
	}
	return fmt.Sprintf("%.2f 億元", *thousands/100_000)
}

// fScoreText 以「分數/可判定項數」表示 F 分數;無資料回「無」。
func fScoreText(score, evaluated *int) string {
	if score == nil {
		return "無"
	}
	if evaluated == nil {
		return fmt.Sprint(*score)
	}
	return fmt.Sprintf("%d/%d 項", *score, *evaluated)
}
