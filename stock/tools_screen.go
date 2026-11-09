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
	"slices"
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
	ExcludeIndustryIDs      []int    `json:"exclude_industry_ids,omitempty"`
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
	// MaxPE、MaxPB 是最新一筆日線(10 天內)的官方本益比、股價淨值比上限;虧損或沒有資料的股票不符合。
	MaxPE *float64 `json:"max_pe,omitempty"`
	MaxPB *float64 `json:"max_pb,omitempty"`
	// MinMajorChange 是集保千張大戶持股比例,最近一週相對 MajorChangeWeeks 週前的變化下限(百分點);週數 0 為預設 4 週。
	MinMajorChange   *float64 `json:"min_major_change,omitempty"`
	MajorChangeWeeks int      `json:"major_change_weeks,omitempty"`
	// MinHolder400Change 是集保 400 張以上大戶持股比例的同樣變化下限(百分點)。
	MinHolder400Change *float64 `json:"min_holder_400_change,omitempty"`
	// MaxMarginChangePercent 是融資餘額近 20 筆變化率上限(%)。
	MaxMarginChangePercent *float64 `json:"max_margin_change_percent,omitempty"`
	// MaxPEG 是 PEG 上限(本益比 ÷ 近四季 EPS 成長率)。
	MaxPEG *float64 `json:"max_peg,omitempty"`
	// RangeBreakoutDays 是近幾個交易日內出現區間突破且還沒跌回突破價(1–20);0 為不篩。
	RangeBreakoutDays int `json:"range_breakout_days,omitempty"`
	// MinEPSYoYPercent、MinAnnualEPSGrowthPercent、NewHighDays 是 CANSLIM 的 C、A、N:最新一季單季 EPS 年增率下限、
	// 近 3 年每年 EPS 年增率下限,以及近幾個日曆天內創過 52 週新高(0 為不篩)。
	MinEPSYoYPercent          *float64 `json:"min_eps_yoy_percent,omitempty"`
	MinAnnualEPSGrowthPercent *float64 `json:"min_annual_eps_growth_percent,omitempty"`
	NewHighDays               int      `json:"new_high_days,omitempty"`
	// MinFillRatePercent、MaxAvgFillDays、MinDividendYears 是存股條件:近 5 年填息率下限、平均填息天數上限、
	// 連續配息年數下限(0 為不篩)。
	MinFillRatePercent *float64 `json:"min_fill_rate_percent,omitempty"`
	MaxAvgFillDays     *float64 `json:"max_avg_fill_days,omitempty"`
	MinDividendYears   int      `json:"min_dividend_years,omitempty"`
	// MinDividendGrowthYears 是股利成長年數下限(1–30);0 為不篩。
	MinDividendGrowthYears int `json:"min_dividend_growth_years,omitempty"`
	// TrendTemplate 為 true 時只留符合 Minervini 趨勢模板的股票。
	TrendTemplate bool `json:"trend_template,omitempty"`
	// MinRevenueAccelerationMonths 是營收加速月數下限(1–11);0 為不篩。
	MinRevenueAccelerationMonths int    `json:"min_revenue_acceleration_months,omitempty"`
	SortBy                       string `json:"sort_by,omitempty"`
	SortOrder                    string `json:"sort_order,omitempty"`
	Limit                        int    `json:"limit,omitempty"`
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
			"exclude_industry_ids": {
				Type:        "array",
				Items:       &jsonschema.Schema{Type: "integer", Minimum: ptr(1.0)},
				MaxItems:    ptr(maxExcludedIndustries),
				Description: "排除的產業代碼(選填,最多 10 個,不可包含 industry_id),例如 [17] 排除金融保險業:金融業營收口徑不同,依毛利率或營業利益率排序時常佔滿前幾名",
			},
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
			"trend_template": {
				Type: "boolean",
				Description: "true 時只留符合 Minervini 趨勢模板的股票(查詢日前 10 天內最新一筆日線):MA20 > MA60 > MA120 > MA240、收盤站上 MA240、不低於 52 週最高價的 75% 且比 52 週最低價高 30% 以上;" +
					"每檔附 trend_template(資料不足為 null)。可搭配 min_rs_score 找相對強勢股",
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
			"max_pe": number(0, 10000,
				"本益比上限(倍):取查詢日之前 10 天內最新一筆日線的官方本益比(證交所/櫃買中心),虧損或沒有資料的股票不符合"),
			"max_pb": number(0, 1000, "股價淨值比上限(倍),資料規則同 max_pe"),
			"min_major_change": number(-100, 100,
				"集保千張大戶持股比例的變化下限(百分點,-100 至 100;例如 1 代表近 major_change_weeks 週增加至少 1 個百分點):"+
					"取查詢日之前已公布的最近一週減去它往前第 major_change_weeks 週,沒有大戶資料的股票不列入"),
			"major_change_weeks": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(12.0),
				Description: "min_major_change 與 min_holder_400_change 的回看週數(集保資料週,1–12,預設 4);兩者都沒設時不影響結果",
			},
			"min_holder_400_change": number(-100, 100,
				"集保 400 張以上大戶持股比例的變化下限(百分點,-100 至 100;籌碼集中):規則同 min_major_change,沒有資料的股票不列入"),
			"max_margin_change_percent": number(-100, 1000,
				"融資餘額變化率上限(%,-100 至 1000;籌碼集中,例如 -5 代表近一個月融資至少減少 5%):查詢日前 10 天內最新一筆對往前第 20 筆;"+
					"基期融資為 0(不能融資或沒有資料)的不列入,每檔附 margin_change_percent"),
			"max_peg": number(0.01, 10, "PEG 上限(0.01 至 10,選填;GARP):本益比 ÷ 近四季 EPS 成長率(%,近四季 Yahoo 單季 EPS 合計對前四季合計,須 8 季相連、前四季合計 > 0);虧損或成長率不在 0～100%(超過 100% 多為低基期轉盈,PEG 失真)的不通過,例如 1 代表本益比不高於成長率;每檔附 eps_ttm_growth_percent、peg_ratio"),
			"range_breakout_days": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(20.0),
				Description: "只留最近 N 個交易日(含查詢日,1–20)出現區間突破、且突破後還沒跌回突破價的股票;" +
					"每檔附最近一次突破的日期與說明。2019 年起回測單獨依區間突破選股明顯輸 0050,約七成突破在 10 個交易日內跌回,只供參考",
			},
			"min_eps_yoy_percent": number(-100, 10000,
				"最新一季單季 EPS 對去年同季的成長率下限(%,CANSLIM 的 C 建議 25);去年同季 EPS ≤ 0 無法計算,這類股票不符合"),
			"min_annual_eps_growth_percent": number(-100, 10000,
				"近 3 個年度 EPS「每年」年增率的下限(%,CANSLIM 的 A 建議 25):取最近 4 個年度 EPS 算出 3 個年增率,以最低者比較;"+
					"年度不連續、任一基期 ≤ 0 或最新年度落後超過 2 年都不符合"),
			"new_high_days": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(365.0),
				Description: "只留最近 N 個日曆天(含查詢日,1–365)內創過 52 週新高的股票(CANSLIM 的 N);每檔附最近一次新高日",
			},
			"min_revenue_acceleration_months": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(11.0),
				Description: "營收加速月數下限(1–11):從最新一個月往回,月營收年增率逐月高於前一個月(月份相連)的月數;例如 2 代表最近 3 個月" +
					"的年增率一個比一個高。每檔附 revenue_acceleration_months;可搭配 min_revenue_yoy_percent 排除年增率仍為負的股票",
			},
			"min_fill_rate_percent": number(0, 100,
				"近 5 年填息率下限(%,存股條件):除息日在查詢日前 5 年內、已觀察滿 120 個交易日的現金除息中,120 個交易日內收盤回到填息價的比例;"+
					"沒有觀察期已滿的除息不符合。每檔都會回傳 fill_rate_percent、avg_fill_days、dividend_years"),
			"max_avg_fill_days": number(1, 120,
				"近 5 年已填息者的平均填息交易日數上限(1–120,存股條件);都沒有填息的不符合"),
			"min_dividend_years": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(30.0),
				Description: "連續配發現金股利的年數下限(1–30,存股條件),以除息日年度計;最近一次除息早於查詢年前一年的不符合。" +
					"可搭配 min_dividend_yield_percent、min_fill_rate_percent 找高殖利率又常填息的股票",
			},
			"min_dividend_growth_years": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(30.0),
				Description: "股利成長年數下限(1–30,股利貴族):以除息日年度加總現金股利,只看查詢年以前的完整年度,從查詢年前一年往回" +
					"每年都不低於前一年(年度相連)的年數;例如 5 代表連續 6 年配息且 5 次都沒減少。每檔附 dividend_growth_years",
			},
			"sort_by": {
				Type:    "string",
				Enum:    []any{"stock_symbol", "revenue_yoy", "revenue_yoy_streak", "revenue_yoy_3m", "monthly_revenue", "eps", "roe", "dividend_yield", "valuation_percentage", "f_score", "gross_margin", "debt_ratio", "operating_margin", "rs_score", "pe_ratio", "pb_ratio", "eps_yoy", "annual_eps_growth", "magic_formula", "peg"},
				Default: []byte(`"stock_symbol"`),
				Description: "固定白名單排序欄位;magic_formula 為神奇公式(Greenblatt)名次合計:盈餘殖利率(近四季營業利益 ÷ 企業價值)名次 + " +
					"資本報酬率(÷ 淨營運資金 + 不動產廠房設備)名次,搭配 sort_order=asc 由好到差;每檔都附 earnings_yield_percent、" +
					"return_on_capital_percent、magic_formula_rank(名次只在本次篩選範圍內比較)、magic_formula_ranked_count(排名母體檔數)與分子分母 " +
					"operating_income_ttm、invested_capital、enterprise_value(千元;金融業沒有資料);peg 為 PEG 由低到高(搭配 asc)。" +
					"magic_formula 與 peg 單獨即可查詢(全市場排行),其他排序仍需篩選條件",
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
	excluded, err := excludedIndustryIDs(in.ExcludeIndustryIDs, in.IndustryID)
	if err != nil {
		return nil, nil, err
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
		{"max_pe", in.MaxPE, 0, 10000},
		{"max_pb", in.MaxPB, 0, 1000},
		{"min_major_change", in.MinMajorChange, -100, 100},
		{"min_holder_400_change", in.MinHolder400Change, -100, 100},
		{"max_margin_change_percent", in.MaxMarginChangePercent, -100, 1000},
		{"max_peg", in.MaxPEG, 0.01, 10},
		{"min_eps_yoy_percent", in.MinEPSYoYPercent, -100, 10000},
		{"min_annual_eps_growth_percent", in.MinAnnualEPSGrowthPercent, -100, 10000},
		{"min_fill_rate_percent", in.MinFillRatePercent, 0, 100},
		{"max_avg_fill_days", in.MaxAvgFillDays, 1, 120},
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
	if in.MajorChangeWeeks != 0 && (in.MajorChangeWeeks < 1 || in.MajorChangeWeeks > 12) {
		return nil, nil, fmt.Errorf("參數 major_change_weeks 必須介於 1 到 12 之間")
	}
	if in.RangeBreakoutDays != 0 && (in.RangeBreakoutDays < 1 || in.RangeBreakoutDays > 20) {
		return nil, nil, fmt.Errorf("參數 range_breakout_days 必須介於 1 到 20 之間")
	}
	if in.NewHighDays != 0 && (in.NewHighDays < 1 || in.NewHighDays > 365) {
		return nil, nil, fmt.Errorf("參數 new_high_days 必須介於 1 到 365 之間")
	}
	if in.MinRevenueAccelerationMonths != 0 && (in.MinRevenueAccelerationMonths < 1 || in.MinRevenueAccelerationMonths > 11) {
		return nil, nil, fmt.Errorf("參數 min_revenue_acceleration_months 必須介於 1 到 11 之間")
	}
	if in.MinDividendGrowthYears != 0 && (in.MinDividendGrowthYears < 1 || in.MinDividendGrowthYears > 30) {
		return nil, nil, fmt.Errorf("參數 min_dividend_growth_years 必須介於 1 到 30 之間")
	}
	if in.MinDividendYears != 0 && (in.MinDividendYears < 1 || in.MinDividendYears > 30) {
		return nil, nil, fmt.Errorf("參數 min_dividend_years 必須介於 1 到 30 之間")
	}

	// all 是預設資料範圍，不會縮小結果，單獨提供不算實質篩選；twse/tpex
	// 會把範圍縮到單一市場，所以本身即可作為必要的篩選條件。
	hasFilter := market != "all" || in.IndustryID > 0 || len(excluded) > 0 || valuationBand != "" ||
		in.MinRevenueYOYPercent != nil || in.MinRevenueYOYStreak != nil || in.MinRevenueYOY3MPercent != nil || in.MinMonthlyRevenue != nil || in.MinEPS != nil || in.MinROEPercent != nil || in.MinDividendYieldPercent != nil ||
		in.MinFScore != nil || in.MinGrossMarginPercent != nil || in.MaxDebtRatioPercent != nil || in.PositiveFreeCashFlow ||
		in.MinOperatingMarginPercent != nil || in.MarginsYoYUp || in.RevenueRecord ||
		in.MinRSScore != nil || in.MinForeignStreak != nil || in.MinTrustStreak != nil || in.MinAvgTradeValue != nil ||
		in.MaxPE != nil || in.MaxPB != nil || in.MinMajorChange != nil || in.RangeBreakoutDays > 0 ||
		in.MinHolder400Change != nil || in.MaxMarginChangePercent != nil || in.MaxPEG != nil || rankingSort(in.SortBy) ||
		in.MinEPSYoYPercent != nil || in.MinAnnualEPSGrowthPercent != nil || in.NewHighDays > 0 ||
		in.MinFillRatePercent != nil || in.MaxAvgFillDays != nil || in.MinDividendYears > 0 ||
		in.MinRevenueAccelerationMonths > 0 || in.MinDividendGrowthYears > 0 || in.TrendTemplate
	if !hasFilter {
		return nil, nil, fmt.Errorf("至少需要一個實質篩選條件；market=all、limit 與 magic_formula／peg 以外的排序不算篩選條件")
	}

	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if sortBy == "" {
		sortBy = "stock_symbol"
	}
	validSort := map[string]bool{
		"stock_symbol": true, "revenue_yoy": true, "revenue_yoy_streak": true, "revenue_yoy_3m": true, "monthly_revenue": true, "eps": true, "roe": true, "dividend_yield": true, "valuation_percentage": true,
		"f_score": true, "gross_margin": true, "debt_ratio": true, "operating_margin": true, "rs_score": true,
		"pe_ratio": true, "pb_ratio": true, "eps_yoy": true, "annual_eps_growth": true, "magic_formula": true, "peg": true,
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
		Market: market, IndustryID: in.IndustryID, ExcludeIndustryIDs: excluded, ValuationBand: valuationBand,
		MinRevenueYOYPercent: in.MinRevenueYOYPercent, MinRevenueYOYStreak: in.MinRevenueYOYStreak,
		MinRevenueYOY3MPercent: in.MinRevenueYOY3MPercent, MinMonthlyRevenue: in.MinMonthlyRevenue, MinEPS: in.MinEPS,
		MinROEPercent: in.MinROEPercent, MinDividendYieldPercent: in.MinDividendYieldPercent,
		MinFScore: in.MinFScore, MinGrossMarginPercent: in.MinGrossMarginPercent,
		MaxDebtRatioPercent: in.MaxDebtRatioPercent, PositiveFreeCashFlow: in.PositiveFreeCashFlow,
		MinOperatingMarginPercent: in.MinOperatingMarginPercent, MarginsYoYUp: in.MarginsYoYUp, RevenueRecord: in.RevenueRecord,
		MinRSScore: in.MinRSScore, MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak,
		MinAvgTradeValue: in.MinAvgTradeValue, MaxPE: in.MaxPE, MaxPB: in.MaxPB,
		MinMajorChange: in.MinMajorChange, MajorChangeWeeks: in.MajorChangeWeeks, RangeBreakoutDays: in.RangeBreakoutDays,
		MinHolder400Change: in.MinHolder400Change, MaxMarginChangePercent: in.MaxMarginChangePercent, MaxPEG: in.MaxPEG,
		MinEPSYoYPercent: in.MinEPSYoYPercent, MinAnnualEPSGrowthPercent: in.MinAnnualEPSGrowthPercent, NewHighDays: in.NewHighDays,
		MinFillRatePercent: in.MinFillRatePercent, MaxAvgFillDays: in.MaxAvgFillDays, MinDividendYears: in.MinDividendYears,
		MinRevenueAccelerationMonths: in.MinRevenueAccelerationMonths,
		MinDividendGrowthYears:       in.MinDividendGrowthYears,
		TrendTemplate:                in.TrendTemplate,
		SortBy:                       sortBy, SortOrder: sortOrder, Limit: limit,
	})
	if err != nil {
		ts.logf("工具 screen_stocks 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定條件下沒有符合的股票。此工具只描述歷史資料符合情形,不替使用者做投資決策。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Stocks) > 0 {
		first := envelope.Stocks[0]
		summary = fmt.Sprintf("依固定條件篩選出 %d 檔股票；排序首筆為 %s %s（營收年增率 %s、連續年增 %s 個月、近 3 月累計年增 %s、最新月營收 %s（%s）、EPS %s、ROE %s、殖利率 %s、F 分數 %s（%s 年）、毛利率 %s、營業利益率 %s、淨利率 %s、%s、負債比 %s、近四季自由現金流 %s、相對強度 %s（%s）、外資%s、投信%s（%s）、近 20 日平均成交 %s、本益比 %s、股價淨值比 %s（%s）；營收期 %s、財報期 %s、估值日 %s、殖利率日 %s、季報指標期 %s）。各股票指標日期可能不同,結果不構成買賣建議。\n免責聲明:%s",
			len(envelope.Stocks), first.StockSymbol, first.Name,
			displayPercent(first.RevenueYOYPercent), displayInt(first.RevenueYOYStreak), displayPercent(first.RevenueYOY3MPercent), thousandsToHundredMillion(first.MonthlyRevenue), revenueRecordText(first.RevenueRecord), displayFloat(first.EarningsPerShare), displayPercent(first.ReturnOnEquity), displayPercent(first.DividendYieldPercent),
			fScoreText(first.FScore, first.FScoreEvaluated), displayInt(first.FScoreYear),
			displayPercent(first.GrossMarginPercent), displayPercent(first.OperatingMarginPercent), displayPercent(first.NetMarginPercent),
			marginsYoYText(first.MarginsYoYUp, first.MarginPeriod), displayPercent(first.DebtRatioPercent), hundredMillion(first.FreeCashFlowTTM),
			displayInt(first.RSScore), displayString(envelope.RSAsOf),
			streakDisplay(first.ForeignStreak), streakDisplay(first.TrustStreak), displayString(envelope.ChipAsOf),
			millionText(first.AvgTradeValue20D), displayFloat(first.PERatio), displayFloat(first.PBRatio), displayString(first.RatioDate),
			displayString(first.RevenueMonth), displayString(first.FinancialPeriod), displayString(first.ValuationDate), displayString(first.YieldDate),
			displayString(first.HealthPeriod), AnalysisDisclaimer)
		if line := screenBreakoutLine(in.RangeBreakoutDays, first); line != "" {
			summary = line + "\n" + summary
		}
		if line := screenGrowthLine(first); line != "" {
			summary = line + "\n" + summary
		}
		if line := screenPEGLine(first); line != "" {
			summary = line + "\n" + summary
		}
		if line := screenMagicFormulaLine(first); line != "" {
			summary = line + "\n" + summary
		}
		if line := screenDividendLine(first); line != "" {
			summary = line + "\n" + summary
		}
		if in.TrendTemplate {
			summary = "篩選條件:" + trendTemplateText + "。\n" + summary
		}
	}
	return textResult(summary), ScreeningOutput{
		DataKind: "stock_screening_result", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, RSAsOf: envelope.RSAsOf, ChipAsOf: envelope.ChipAsOf, Stocks: envelope.Stocks,
	}, nil
}

// maxExcludedIndustries 是排除產業的上限,與 Data API 相同。
const maxExcludedIndustries = 10

// excludedIndustryIDs 驗證排除的產業代碼並去除重複(保留順序):須為正整數、最多 10 個、不可包含 industry_id。
func excludedIndustryIDs(ids []int, industryID int) ([]int, error) {
	var excluded []int
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("參數 exclude_industry_ids 必須是正整數產業代碼")
		}
		if id == industryID {
			return nil, fmt.Errorf("參數 exclude_industry_ids 不可包含 industry_id 指定的產業")
		}
		if !slices.Contains(excluded, id) {
			excluded = append(excluded, id)
		}
	}
	if len(excluded) > maxExcludedIndustries {
		return nil, fmt.Errorf("參數 exclude_industry_ids 最多 %d 個", maxExcludedIndustries)
	}
	return excluded, nil
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

// millionText 將百萬元來源交給共用金額格式器。
func millionText(million *float64) string {
	return FormatCurrencyWithUnit(million, CurrencyMillion)
}

// thousandsToHundredMillion 將千元來源交給共用金額格式器。
func thousandsToHundredMillion(thousands *float64) string {
	return FormatCurrencyWithUnit(thousands, CurrencyThousand)
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

// screenGrowthLine 描述首筆的 CANSLIM 成長指標(單季 EPS 年增、近 3 年最低年增、最近一次 52 週新高);
// 三者都沒有資料時回空字串,避免一般選股的摘要多一行「無」。
func screenGrowthLine(first ScreenedStock) string {
	if first.EPSYoYPercent == nil && first.AnnualEPSGrowthPercent == nil && first.NewHighDate == nil {
		return ""
	}
	annual := displayPercent(first.AnnualEPSGrowthPercent)
	if first.AnnualEPSGrowthPercent != nil && first.AnnualEPSYear != nil {
		annual = fmt.Sprintf("%s(至 %d 年)", annual, *first.AnnualEPSYear)
	}
	return fmt.Sprintf("首筆成長指標:單季 EPS 年增 %s、近 3 年每年 EPS 年增最低 %s、最近一次 52 週新高 %s。",
		displayPercent(first.EPSYoYPercent), annual, displayString(first.NewHighDate))
}

// screenDividendLine 描述首筆的存股指標(近 5 年填息率、平均填息天數、連續配息年數,有股利成長年數時一併附上);
// 沒有填息率且沒有連續配息時回空字串(舊版 API 或不配息的股票)。
func screenDividendLine(first ScreenedStock) string {
	if first.FillRatePercent == nil && (first.DividendYears == nil || *first.DividendYears == 0) {
		return ""
	}
	growth := ""
	if first.DividendGrowthYears != nil {
		growth = fmt.Sprintf("、股利連續不減少 %d 年", *first.DividendGrowthYears)
	}
	return fmt.Sprintf("首筆存股指標:近 5 年填息率 %s、平均填息 %s 個交易日、連續配息 %s 年%s。",
		displayPercent(first.FillRatePercent), displayFloat(first.AvgFillDays), displayInt(first.DividendYears), growth)
}

// extremeReturnOnCapital 是資本報酬率提示的門檻(%):超過時投入資本相對營業利益很小,比率容易失真。
const extremeReturnOnCapital = 200.0

// rankingSort 回報排序本身是否就是全市場排行(神奇公式、PEG),不必另設篩選條件。
func rankingSort(sortBy string) bool {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "magic_formula", "peg":
		return true
	}
	return false
}

// screenMagicFormulaLine 描述首筆的神奇公式指標(盈餘殖利率、資本報酬率、名次合計與排名母體,以及分子分母);
// 兩個比率都沒有時回空字串(金融業、資料不足或舊版 API)。資本報酬率超過 extremeReturnOnCapital 時附提示。
func screenMagicFormulaLine(first ScreenedStock) string {
	if first.EarningsYieldPercent == nil && first.ReturnOnCapitalPercent == nil {
		return ""
	}
	line := fmt.Sprintf("首筆神奇公式:盈餘殖利率 %s、資本報酬率 %s、名次合計 %s(越小越好,在本次篩選後 %s 檔有資料的股票中排名,"+
		"放寬或收緊條件名次會變);近四季營業利益 %s、投入資本 %s、企業價值 %s。",
		displayPercent(first.EarningsYieldPercent), displayPercent(first.ReturnOnCapitalPercent), displayInt(first.MagicFormulaRank),
		displayInt(first.MagicFormulaRankedCount), thousandsToHundredMillion(first.OperatingIncomeTTM),
		thousandsToHundredMillion(first.InvestedCapital), thousandsToHundredMillion(first.EnterpriseValue))
	if roc := first.ReturnOnCapitalPercent; roc != nil && *roc > extremeReturnOnCapital {
		line += "資本報酬率極高:投入資本(淨營運資金 + 不動產廠房設備)相對營業利益很小,比率容易失真,宜一併看盈餘殖利率。"
	}
	return line
}

// screenPEGLine 描述首筆的 PEG 與近四季 EPS 成長率;兩者都沒有時回空字串(資料不足或舊版 API)。
func screenPEGLine(first ScreenedStock) string {
	if first.PEGRatio == nil && first.EPSTTMGrowthPercent == nil {
		return ""
	}
	return fmt.Sprintf("首筆 PEG:%s(本益比 %s ÷ 近四季 EPS 成長率 %s;虧損、衰退或成長逾 100%% 時無)。",
		displayFloat(first.PEGRatio), displayFloat(first.PERatio), displayPercent(first.EPSTTMGrowthPercent))
}

// screenBreakoutLine 是設了 range_breakout_days 時,摘要開頭說明條件與首筆的突破日;沒設或首筆沒有突破日時回空字串。
func screenBreakoutLine(days int, first ScreenedStock) string {
	if days <= 0 || first.RangeBreakoutDate == nil {
		return ""
	}
	return fmt.Sprintf("區間突破條件:近 %d 個交易日突破且還沒跌回;首筆最近一次突破 %s(%s)。回測單獨使用明顯輸 0050,只供參考。",
		days, *first.RangeBreakoutDate, displayString(first.RangeBreakoutDescription))
}
