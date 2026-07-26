package stock

// 本檔案是 Phase 1:個股歷史財務工具(月營收/財報/股利)的完整實作:輸入型別、JSON Schema、
// 驗證輔助函式、structuredContent 輸出型別與 handler。
//
// 這些工具由 tools.go 的 AddTools 依「資料來源是否具備對應能力介面」
// 動態註冊;共用的驗證與輸出輔助函式(normalizeSymbol、rangedLimit、
// textResult、displayFloat 等)也都定義在 tools.go。

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Phase 1:個股歷史財務工具(月營收/財報/股利)
// ---------------------------------------------------------------------------
//
// 以下是 get_monthly_revenue_history、get_financial_statement_history、
// get_dividend_history 三個工具的完整實作:輸入型別、JSON Schema、驗證
// 輔助函式、structuredContent 輸出型別與 handler。結構與上面四個既有
// 工具完全相同,新讀者建議先讀懂 searchStock 再回來看這裡。

// financialToolset 把 FinancialQuerier 與記錄函式綁在一起,原則與
// snapshotToolset 相同:不擴充既有的四方法 Querier 介面,而是為新能力
// 建立獨立的小介面與獨立的 toolset。
type financialToolset struct {
	financials FinancialQuerier
	logf       func(string, ...any)
}

// MonthlyRevenueInput 是 get_monthly_revenue_history 的輸入參數型別。
type MonthlyRevenueInput struct {
	Symbol string `json:"symbol"`
	// From / To 月份區間(格式 YYYY-MM,選填);Limit 筆數上限(選填,
	// 預設 24)。零值(空字串/0)代表呼叫端沒有提供,由 handler 套用預設。
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// StatementHistoryInput 是 get_financial_statement_history 的輸入參數型別。
type StatementHistoryInput struct {
	Symbol string `json:"symbol"`
	// PeriodType 期間類型(選填,預設 quarterly)。
	PeriodType string `json:"period_type,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// DividendHistoryInput 是 get_dividend_history 的輸入參數型別。
type DividendHistoryInput struct {
	Symbol string `json:"symbol"`
	// FromYear / ToYear 股利所屬年度區間(西元,選填)。
	FromYear int `json:"from_year,omitempty"`
	ToYear   int `json:"to_year,omitempty"`
	Limit    int `json:"limit,omitempty"`
}

// monthlyRevenueSchema 描述 get_monthly_revenue_history 的輸入形狀。
func monthlyRevenueSchema() *jsonschema.Schema {
	// 月份的 Pattern 只驗「格式」(四位數-二位數);月份是否介於 01–12
	// 由 parseMonthArg 在程式碼裡進一步驗證,分工與日期參數的
	// datePattern/parseDateArg 相同。
	month := func(desc string) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "string", Pattern: `^\d{4}-\d{2}$`, Description: desc}
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"from":   month("起始月份(格式 YYYY-MM,選填)"),
			"to":     month("結束月份(格式 YYYY-MM,選填)"),
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(120.0),
				Default:     []byte("24"),
				Description: "最大回傳筆數(預設 24,範圍 1 至 120)",
			},
		},
		Required: []string{"symbol"},
	}
}

// statementHistorySchema 描述 get_financial_statement_history 的輸入形狀。
func statementHistorySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"period_type": {
				Type: "string",
				// Enum 列出唯三合法值;Default 必須是「編碼成 JSON 語法」
				// 的位元組(字串要含引號),原理見 searchStockSchema 的說明。
				Enum:        []any{"quarterly", "annual", "all"},
				Default:     []byte(`"quarterly"`),
				Description: "期間類型:quarterly(季報)、annual(年報)或 all(全部)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(40.0),
				Default:     []byte("12"),
				Description: "最大回傳筆數(預設 12,範圍 1 至 40)",
			},
		},
		Required: []string{"symbol"},
	}
}

// dividendHistorySchema 描述 get_dividend_history 的輸入形狀。
//
// 年度上限「目前年度加一」是動態值,JSON Schema 無法表達,schema 只設
// 下限 1990,上限由 validateDividendYears 在程式碼裡檢查。
func dividendHistorySchema() *jsonschema.Schema {
	year := func(desc string) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "integer", Minimum: ptr(1990.0), Description: desc}
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol":    symbolSchema(),
			"from_year": year("起始年度(股利所屬年度,西元,選填)"),
			"to_year":   year("結束年度(股利所屬年度,西元,選填)"),
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(80.0),
				Default:     []byte("20"),
				Description: "最大回傳筆數(預設 20,範圍 1 至 80)",
			},
		},
		Required: []string{"symbol"},
	}
}

// monthPattern 驗證月份字串格式(四位數-二位數,例如 "2026-06");
// 月份數值是否介於 01–12 由 parseMonthArg 進一步檢查。
var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// parseMonthArg 驗證 YYYY-MM 格式的月份參數;空字串代表未提供,回傳
// ("", nil)。驗證通過時原樣回傳(伺服器端吃同一種格式,不需轉換)。
func parseMonthArg(raw, field string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if !monthPattern.MatchString(raw) {
		return "", fmt.Errorf("參數 %s 月份格式必須為 YYYY-MM(月份 01 至 12)", field)
	}
	return raw, nil
}

// maxDividendYear 回傳目前允許查詢的最大股利年度:今年加一。
//
// 股利政策常於年初就公布下一年度的配息,因此允許查到明年;這個上限是
// 動態值,JSON Schema 無法表達,只能在程式碼裡檢查。
func maxDividendYear() int {
	return time.Now().Year() + 1
}

// validateDividendYears 驗證股利年度區間:合法範圍是 1990 到 maxYear,
// 且 from 不可晚於 to。0 值代表未提供,不檢查。
//
// maxYear 由呼叫端傳入(而不是在函式內部呼叫 time.Now()),讓這個函式
// 成為一個沒有任何外部相依的純函式:給定相同輸入永遠得到相同結果。
// 這對驗證邏輯特別有價值——測試可以精確指定邊界年度、直接驗證「剛好
// 等於上限」與「超過上限一年」這兩個關鍵案例,而不需要依賴測試執行當下
// 是西元幾年(那種測試會在跨年時莫名其妙地開始失敗或失去意義)。
func validateDividendYears(fromYear, toYear, maxYear int) error {
	for _, year := range []int{fromYear, toYear} {
		if year != 0 && (year < 1990 || year > maxYear) {
			return fmt.Errorf("參數年度必須介於 1990 到 %d 之間,收到了 %d", maxYear, year)
		}
	}
	if fromYear != 0 && toYear != 0 && fromYear > toYear {
		return fmt.Errorf("參數 from_year 不可晚於 to_year")
	}
	return nil
}

// MonthlyRevenueOutput 是 get_monthly_revenue_history 的 structuredContent:
// 依計畫 §3.4,在 Data API envelope(stock_symbol、data_as_of、revenues)
// 之外加上 data_kind、is_realtime、disclaimer 三個共通欄位,不改名、
// 不重排內層資料。
type MonthlyRevenueOutput struct {
	DataKind    string           `json:"data_kind"`
	IsRealtime  bool             `json:"is_realtime"`
	Disclaimer  string           `json:"disclaimer"`
	StockSymbol string           `json:"stock_symbol"`
	DataAsOf    *string          `json:"data_as_of"`
	Revenues    []MonthlyRevenue `json:"revenues"`
}

// StatementHistoryOutput 是 get_financial_statement_history 的 structuredContent。
type StatementHistoryOutput struct {
	DataKind    string               `json:"data_kind"`
	IsRealtime  bool                 `json:"is_realtime"`
	Disclaimer  string               `json:"disclaimer"`
	StockSymbol string               `json:"stock_symbol"`
	DataAsOf    *string              `json:"data_as_of"`
	Statements  []FinancialStatement `json:"statements"`
}

// DividendHistoryOutput 是 get_dividend_history 的 structuredContent。
type DividendHistoryOutput struct {
	DataKind    string     `json:"data_kind"`
	IsRealtime  bool       `json:"is_realtime"`
	Disclaimer  string     `json:"disclaimer"`
	StockSymbol string     `json:"stock_symbol"`
	DataAsOf    *string    `json:"data_as_of"`
	Dividends   []Dividend `json:"dividends"`
}

// monthlyRevenueHistory 執行 get_monthly_revenue_history:驗證輸入 →
// 呼叫 Data API → 組出摘要與結構化輸出。
//
// MCP protocol-level error（例如不存在的工具）由 SDK 處理；此 handler
// 回傳的 Go error 會成為 tool-level error。參數錯誤與股票不存在可安全
// 告知使用者；認證、timeout、5xx、JSON 解析等內部錯誤只寫 server log，
// MCP 一律收到通用訊息，避免暴露內網位址或實作細節。
func (ts *financialToolset) monthlyRevenueHistory(ctx context.Context, _ *mcp.CallToolRequest, in MonthlyRevenueInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	from, err := parseMonthArg(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseMonthArg(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	// YYYY-MM 是零填充的固定寬度格式,字典序即時間序,直接比較字串
	// 就能判斷先後,不需要解析成時間型別。
	if from != "" && to != "" && from > to {
		return nil, nil, fmt.Errorf("起始月份 (from) 不可晚於結束月份 (to)")
	}
	limit, err := rangedLimit(in.Limit, 24, 1, 120)
	if err != nil {
		return nil, nil, err
	}

	history, err := ts.financials.MonthlyRevenueHistory(ctx, symbol, RevenueHistoryOptions{From: from, To: to, Limit: limit})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_monthly_revenue_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	var summary string
	if len(history.Revenues) == 0 {
		summary = fmt.Sprintf("股票 %s 在指定範圍內沒有月營收資料。\n免責聲明:%s", symbol, AnalysisDisclaimer)
	} else {
		latest := history.Revenues[0]
		summary = fmt.Sprintf("取得股票 %s 共 %d 筆月營收(最新 %s:當月營收 %s 仟元,年增率 %s%%)。\n免責聲明:%s",
			symbol, len(history.Revenues), latest.Month, displayFloat(latest.MonthlyRevenue), displayFloat(latest.YearOverYearPercent), AnalysisDisclaimer)
	}
	out := MonthlyRevenueOutput{
		DataKind:    "monthly_revenue_history",
		IsRealtime:  false,
		Disclaimer:  AnalysisDisclaimer,
		StockSymbol: history.StockSymbol,
		DataAsOf:    history.DataAsOf,
		Revenues:    history.Revenues,
	}
	return textResult(summary), out, nil
}

// financialStatementHistory 執行 get_financial_statement_history。
//
// SDK 會把本方法回傳的 Go error 包裝成 MCP tool-level error，而不是
// protocol-level error。只有輸入驗證與 ErrStockNotFound 可直接回給
// 呼叫端；API 認證、timeout、5xx 或解析錯誤只記錄並改回安全通用訊息。
func (ts *financialToolset) financialStatementHistory(ctx context.Context, _ *mcp.CallToolRequest, in StatementHistoryInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	// 空字串代表未提供,套用預設 quarterly;非法值直接報錯,不猜測。
	periodType := in.PeriodType
	if periodType == "" {
		periodType = "quarterly"
	}
	if periodType != "quarterly" && periodType != "annual" && periodType != "all" {
		return nil, nil, fmt.Errorf("參數 period_type 必須為 quarterly、annual 或 all,收到了 %q", in.PeriodType)
	}
	limit, err := rangedLimit(in.Limit, 12, 1, 40)
	if err != nil {
		return nil, nil, err
	}

	history, err := ts.financials.FinancialStatementHistory(ctx, symbol, StatementHistoryOptions{PeriodType: periodType, Limit: limit})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_financial_statement_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	var summary string
	if len(history.Statements) == 0 {
		summary = fmt.Sprintf("股票 %s 在指定條件下沒有財報資料。\n免責聲明:%s", symbol, AnalysisDisclaimer)
	} else {
		latest := history.Statements[0]
		summary = fmt.Sprintf("取得股票 %s 共 %d 筆財報(最新 %d-%s:EPS %s 元,ROE %s%%,毛利率 %s%%)。\n免責聲明:%s",
			symbol, len(history.Statements), latest.Year, latest.Quarter,
			displayFloat(latest.EarningsPerShare), displayFloat(latest.ReturnOnEquity), displayFloat(latest.GrossProfitMargin), AnalysisDisclaimer)
	}
	out := StatementHistoryOutput{
		DataKind:    "financial_statement_history",
		IsRealtime:  false,
		Disclaimer:  AnalysisDisclaimer,
		StockSymbol: history.StockSymbol,
		DataAsOf:    history.DataAsOf,
		Statements:  history.Statements,
	}
	return textResult(summary), out, nil
}

// dividendHistory 執行 get_dividend_history。
//
// MCP protocol-level error 由 SDK 負責；本 handler 的 error 屬於
// tool-level error。可修正的年度/筆數輸入與股票不存在會回明確訊息，
// 其餘 Data API 細節只進 server log，對外固定使用安全通用錯誤。
func (ts *financialToolset) dividendHistory(ctx context.Context, _ *mcp.CallToolRequest, in DividendHistoryInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	if err := validateDividendYears(in.FromYear, in.ToYear, maxDividendYear()); err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 80)
	if err != nil {
		return nil, nil, err
	}

	history, err := ts.financials.DividendHistory(ctx, symbol, DividendHistoryOptions{FromYear: in.FromYear, ToYear: in.ToYear, Limit: limit})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_dividend_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	var summary string
	if len(history.Dividends) == 0 {
		summary = fmt.Sprintf("股票 %s 在指定範圍內沒有股利資料。\n免責聲明:%s", symbol, AnalysisDisclaimer)
	} else {
		latest := history.Dividends[0]
		summary = fmt.Sprintf("取得股票 %s 共 %d 筆股利資料(最新 %d-%s:現金股利 %s 元,股票股利 %s 元,除息日 %s)。\n免責聲明:%s",
			symbol, len(history.Dividends), latest.DividendYear, latest.Quarter,
			displayFloat(latest.CashDividend), displayFloat(latest.StockDividend), displayString(latest.ExDividendDate), AnalysisDisclaimer)
	}
	out := DividendHistoryOutput{
		DataKind:    "dividend_history",
		IsRealtime:  false,
		Disclaimer:  AnalysisDisclaimer,
		StockSymbol: history.StockSymbol,
		DataAsOf:    history.DataAsOf,
		Dividends:   history.Dividends,
	}
	return textResult(summary), out, nil
}
