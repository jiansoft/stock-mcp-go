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

// screenToolset 綁定 StockScreener 與安全記錄函式。
type screenToolset struct {
	screener StockScreener
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
	MinEPS                  *float64 `json:"min_eps,omitempty"`
	MinROEPercent           *float64 `json:"min_roe_percent,omitempty"`
	MinDividendYieldPercent *float64 `json:"min_dividend_yield_percent,omitempty"`
	SortBy                  string   `json:"sort_by,omitempty"`
	SortOrder               string   `json:"sort_order,omitempty"`
	Limit                   int      `json:"limit,omitempty"`
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
			"min_revenue_yoy_percent":    number(-100, 10000, "最低月營收年增率百分比"),
			"min_eps":                    number(-10000, 10000, "最低每股盈餘 EPS"),
			"min_roe_percent":            number(-10000, 10000, "最低股東權益報酬率百分比"),
			"min_dividend_yield_percent": number(0, 1000, "最低殖利率百分比"),
			"sort_by": {
				Type:        "string",
				Enum:        []any{"stock_symbol", "revenue_yoy", "eps", "roe", "dividend_yield", "valuation_percentage"},
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

// StockScreeningOutput 是 Data API envelope 加上 MCP 分析型共通欄位。
type StockScreeningOutput struct {
	DataKind   string          `json:"data_kind"`
	IsRealtime bool            `json:"is_realtime"`
	Disclaimer string          `json:"disclaimer"`
	DataAsOf   *string         `json:"data_as_of"`
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
		{"min_eps", in.MinEPS, -10000, 10000},
		{"min_roe_percent", in.MinROEPercent, -10000, 10000},
		{"min_dividend_yield_percent", in.MinDividendYieldPercent, 0, 1000},
	} {
		if err := validateOptionalFloat(check.field, check.value, check.minimum, check.max); err != nil {
			return nil, nil, err
		}
	}

	// all 是預設資料範圍，不會縮小結果，單獨提供不算實質篩選；twse/tpex
	// 會把範圍縮到單一市場，所以本身即可作為必要的篩選條件。
	hasFilter := market != "all" || in.IndustryID > 0 || valuationBand != "" ||
		in.MinRevenueYOYPercent != nil || in.MinEPS != nil || in.MinROEPercent != nil || in.MinDividendYieldPercent != nil
	if !hasFilter {
		return nil, nil, fmt.Errorf("至少需要一個實質篩選條件；market=all、排序與 limit 不算篩選條件")
	}

	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if sortBy == "" {
		sortBy = "stock_symbol"
	}
	validSort := map[string]bool{"stock_symbol": true, "revenue_yoy": true, "eps": true, "roe": true, "dividend_yield": true, "valuation_percentage": true}
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
		MinRevenueYOYPercent: in.MinRevenueYOYPercent, MinEPS: in.MinEPS,
		MinROEPercent: in.MinROEPercent, MinDividendYieldPercent: in.MinDividendYieldPercent,
		SortBy: sortBy, SortOrder: sortOrder, Limit: limit,
	})
	if err != nil {
		ts.logf("工具 screen_stocks 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定條件下沒有符合的股票。此工具只描述歷史資料符合情形,不替使用者做投資決策。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Stocks) > 0 {
		first := envelope.Stocks[0]
		summary = fmt.Sprintf("依固定條件篩選出 %d 檔股票；排序首筆為 %s %s（營收年增率 %s%%、EPS %s、ROE %s%%、殖利率 %s%%；營收期 %s、財報期 %s、估值日 %s、殖利率日 %s）。各股票指標日期可能不同,結果不構成買賣建議。\n免責聲明:%s",
			len(envelope.Stocks), first.StockSymbol, first.Name,
			displayFloat(first.RevenueYOYPercent), displayFloat(first.EarningsPerShare), displayFloat(first.ReturnOnEquity), displayFloat(first.DividendYieldPercent),
			displayString(first.RevenueMonth), displayString(first.FinancialPeriod), displayString(first.ValuationDate), displayString(first.YieldDate), AnalysisDisclaimer)
	}
	return textResult(summary), StockScreeningOutput{
		DataKind: "stock_screening_result", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Stocks: envelope.Stocks,
	}, nil
}
