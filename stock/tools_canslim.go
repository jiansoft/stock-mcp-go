// Package stock 的 screen_canslim 工具:以威廉·歐尼爾(William O'Neil)的 CANSLIM 七項條件選股。
//
// C、A、N、L 與 I 交給 screen_stocks 的同一組白名單條件(Data API /api/v1/stocks/screen),
// M(大盤方向)讀 get_market_regime 的加權指數相對 60 日均線位置,與回測 market_filter_ma=60 同一判斷,只供參考。
// S(供需)的「流通股數少」無法直接量化,只提供 min_avg_trade_value 排除買不到量的股票。
//
// 預設不篩 A(近 3 年每年 EPS 成長與 ROE):2019-06 起的台股回測顯示 A 與 ROE 17% 讓候選股只剩一兩檔、
// 報酬大幅變差;要套用時帶 include_annual_growth,或直接給 A 的門檻。
package stock

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CANSLIM 預設門檻:歐尼爾原書的數字(EPS 年增 25%、ROE 17%、RS 80),新高回看 60 個日曆天;A 與 ROE 只在要求時套用。
const (
	canslimDefaultEPSYoY        = 25.0
	canslimDefaultAnnualGrowth  = 25.0
	canslimDefaultROE           = 17.0
	canslimDefaultNewHighDays   = 60
	canslimDefaultRSScore       = 80
	canslimMarketMovingAverage  = 60
	canslimDefaultSortBy        = "rs_score"
	canslimDefaultSortDirection = "desc"
)

// canslimBacktestNote 是摘要附帶的台股回測結論(第 37 輪 T28)。
const canslimBacktestNote = "台股回測(2019-06 起、扣成本、換股日偏移中位數):C+N+L 年化 38.4%、夏普 1.07,只看 RS 為 27.8%、0.78;A、ROE 17% 與大盤 60 日均線濾網都讓結果變差,所以 A 預設不篩。具名策略 CANSLIM(C+N+L 加一半 0050)年化 35.5%、夏普 1.27,可用回測工具 preset=CANSLIM 查看"

// canslimSkippable 是可以用 skip_letters 拿掉的字母;S、I、M 預設就不篩選,不需要跳過。
var canslimSkippable = []string{"C", "A", "N", "L"}

// ScreenCanslimInput 是 screen_canslim 的輸入;門檻沒有提供時用歐尼爾的預設值,skip_letters 可整項不篩。
// A(年度 EPS 成長與 ROE)只在 IncludeAnnualGrowth 為 true 或給了 A 的門檻時套用。
type ScreenCanslimInput struct {
	IncludeAnnualGrowth       bool     `json:"include_annual_growth,omitempty"`
	MinEPSYoYPercent          *float64 `json:"min_eps_yoy_percent,omitempty"`
	MinAnnualEPSGrowthPercent *float64 `json:"min_annual_eps_growth_percent,omitempty"`
	MinROEPercent             *float64 `json:"min_roe_percent,omitempty"`
	NewHighDays               int      `json:"new_high_days,omitempty"`
	MinRSScore                int      `json:"min_rs_score,omitempty"`
	MinForeignStreak          *int     `json:"min_foreign_streak,omitempty"`
	MinTrustStreak            *int     `json:"min_trust_streak,omitempty"`
	MinAvgTradeValue          *int     `json:"min_avg_trade_value,omitempty"`
	SkipLetters               []string `json:"skip_letters,omitempty"`
	Market                    string   `json:"market,omitempty"`
	ExcludeIndustryIDs        []int    `json:"exclude_industry_ids,omitempty"`
	SortBy                    string   `json:"sort_by,omitempty"`
	Limit                     int      `json:"limit,omitempty"`
}

// screenCanslimSchema 列出 CANSLIM 各字母的門檻;數值範圍與 screen_stocks 相同,handler 交給它再驗證一次。
func screenCanslimSchema() *jsonschema.Schema {
	base := screenStocksSchema().Properties
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"min_eps_yoy_percent": {
				Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0), Default: []byte("25"),
				Description: "C:最新一季單季 EPS 對去年同季的成長率下限(%,預設 25);去年同季 EPS ≤ 0 的股票不符合",
			},
			"min_annual_eps_growth_percent": {
				Type: "number", Minimum: ptr(-100.0), Maximum: ptr(10000.0), Default: []byte("25"),
				Description: "A:近 3 個年度 EPS 每年年增率的下限(%,套用 A 時預設 25;取三年中最低的一年比較)。給了這個值就會套用 A",
			},
			"include_annual_growth": {
				Type:        "boolean",
				Description: "true 時套用 A(近 3 年每年 EPS 年增與 ROE,預設 25%／17%)。預設不篩:台股回測 A 與 ROE 17% 讓結果明顯變差",
			},
			"min_roe_percent": {
				Type: "number", Minimum: ptr(-10000.0), Maximum: ptr(10000.0), Default: []byte("17"),
				Description: "A:最新一季股東權益報酬率下限(%,套用 A 時預設 17);給了這個值就會套用 A,skip_letters 含 A 時一併不篩",
			},
			"new_high_days": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(365.0), Default: []byte("60"),
				Description: "N:最近 N 個日曆天內創過 52 週新高(預設 60);新產品、新經營層等質化條件無法量化,以股價新高代表",
			},
			"min_rs_score": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(99.0), Default: []byte("80"),
				Description: "L:相對強度分數下限(1–99,預設 80)",
			},
			"min_foreign_streak": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0),
				Description: "I(選填):外資至少連續買超幾個交易日;預設不篩,結果仍附外資與投信的連續天數",
			},
			"min_trust_streak": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(60.0),
				Description: "I(選填):投信至少連續買超幾個交易日;預設不篩",
			},
			"min_avg_trade_value": {
				Type: "integer", Minimum: ptr(1.0), Maximum: ptr(100000.0),
				Description: "S(選填):近 20 筆日線平均成交金額下限(百萬元);歐尼爾偏好的「流通股數少」無法直接量化,這裡只排除買不到量的股票",
			},
			"skip_letters": {
				Type:        "array",
				Items:       &jsonschema.Schema{Type: "string", Enum: []any{"C", "A", "N", "L"}},
				MaxItems:    ptr(4),
				Description: "不篩選的字母(選填):C、A(連同 ROE)、N、L;A 預設就不篩,列在這裡時連明確給的 A 門檻也忽略",
			},
			"market":               base["market"],
			"exclude_industry_ids": base["exclude_industry_ids"],
			"sort_by":              withDefault(base["sort_by"], canslimDefaultSortBy),
			"limit":                base["limit"],
		},
	}
}

// withDefault 複製一份 schema 並改預設值,避免改到 screen_stocks 共用的物件。
func withDefault(schema *jsonschema.Schema, value string) *jsonschema.Schema {
	copied := *schema
	copied.Default = []byte(fmt.Sprintf("%q", value))
	return &copied
}

// CanslimMarket 是 M(大盤方向)的判斷:加權指數收盤是否在 60 日均線之上;regime 查不到時 Above 為 nil。
type CanslimMarket struct {
	DataAsOf  *string  `json:"data_as_of"`
	Close     *float64 `json:"close"`
	MA60      *float64 `json:"ma60"`
	Above     *bool    `json:"above_ma60"`
	Alignment *string  `json:"alignment"`
}

// CanslimOutput 是 screen_canslim 的 structuredContent:套用的門檻、大盤判斷與選股結果。
type CanslimOutput struct {
	DataKind   string          `json:"data_kind"`
	IsRealtime bool            `json:"is_realtime"`
	Disclaimer string          `json:"disclaimer"`
	Criteria   []string        `json:"criteria"`
	Skipped    []string        `json:"skipped_letters"`
	SortBy     string          `json:"sort_by"`
	Market     CanslimMarket   `json:"market"`
	RSAsOf     *string         `json:"rs_as_of"`
	ChipAsOf   *string         `json:"chip_as_of"`
	Stocks     []ScreenedStock `json:"stocks"`
}

// canslimToolset 綁定選股與大盤趨勢兩個資料來源。
type canslimToolset struct {
	screener Screener
	regime   MarketRegimeQuerier
	logf     func(string, ...any)
}

// screenCanslim 執行 screen_canslim:組出 screen_stocks 的條件並查大盤 60 日均線位置。
func (ts *canslimToolset) screenCanslim(ctx context.Context, req *mcp.CallToolRequest, in ScreenCanslimInput) (*mcp.CallToolResult, any, error) {
	skipped, err := canslimSkipped(in)
	if err != nil {
		return nil, nil, err
	}
	screen, criteria := canslimScreenInput(in, skipped)
	inner := &screenToolset{screener: ts.screener, logf: ts.logf}
	_, structured, err := inner.screenStocks(ctx, req, screen)
	if err != nil {
		return nil, nil, err
	}
	// screenStocks 成功時一定回傳 ScreeningOutput。
	result, _ := structured.(ScreeningOutput)
	market := ts.marketDirection(ctx)
	output := CanslimOutput{
		DataKind: "canslim_screening_result", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		Criteria: criteria, Skipped: skipped, SortBy: screen.SortBy, Market: market,
		RSAsOf: result.RSAsOf, ChipAsOf: result.ChipAsOf, Stocks: result.Stocks,
	}
	return textResult(canslimSummary(output)), output, nil
}

// canslimSkipped 正規化 skip_letters(去空白、轉大寫、去重複);不在 C、A、N、L 之內回錯誤。
// 沒有要求 A(include_annual_growth 或 A 的門檻)時把 A 加進跳過清單。
func canslimSkipped(in ScreenCanslimInput) ([]string, error) {
	var skipped []string
	for _, raw := range in.SkipLetters {
		letter := strings.ToUpper(strings.TrimSpace(raw))
		if !slices.Contains(canslimSkippable, letter) {
			return nil, fmt.Errorf("參數 skip_letters 只能包含 C、A、N、L")
		}
		if !slices.Contains(skipped, letter) {
			skipped = append(skipped, letter)
		}
	}
	wantsAnnual := in.IncludeAnnualGrowth || in.MinAnnualEPSGrowthPercent != nil || in.MinROEPercent != nil
	if !wantsAnnual && !slices.Contains(skipped, "A") {
		skipped = append(skipped, "A")
	}
	return skipped, nil
}

// canslimScreenInput 依預設值、呼叫端門檻與跳過的字母組出 screen_stocks 輸入,並回傳每個條件的中文說明。
func canslimScreenInput(in ScreenCanslimInput, skipped []string) (ScreenStocksInput, []string) {
	screen := ScreenStocksInput{
		Market: in.Market, ExcludeIndustryIDs: in.ExcludeIndustryIDs,
		MinForeignStreak: in.MinForeignStreak, MinTrustStreak: in.MinTrustStreak, MinAvgTradeValue: in.MinAvgTradeValue,
		SortBy: in.SortBy, SortOrder: canslimDefaultSortDirection, Limit: in.Limit,
	}
	if screen.SortBy == "" {
		screen.SortBy = canslimDefaultSortBy
	}
	var criteria []string
	if !slices.Contains(skipped, "C") {
		screen.MinEPSYoYPercent = ptr(floatOr(in.MinEPSYoYPercent, canslimDefaultEPSYoY))
		criteria = append(criteria, fmt.Sprintf("C 單季 EPS 年增 ≥ %s%%", formatFloat(*screen.MinEPSYoYPercent)))
	}
	if !slices.Contains(skipped, "A") {
		screen.MinAnnualEPSGrowthPercent = ptr(floatOr(in.MinAnnualEPSGrowthPercent, canslimDefaultAnnualGrowth))
		screen.MinROEPercent = ptr(floatOr(in.MinROEPercent, canslimDefaultROE))
		criteria = append(criteria,
			fmt.Sprintf("A 近 3 年每年 EPS 年增 ≥ %s%%", formatFloat(*screen.MinAnnualEPSGrowthPercent)),
			fmt.Sprintf("A ROE ≥ %s%%", formatFloat(*screen.MinROEPercent)))
	}
	if !slices.Contains(skipped, "N") {
		screen.NewHighDays = intOr(in.NewHighDays, canslimDefaultNewHighDays)
		criteria = append(criteria, fmt.Sprintf("N 近 %d 天創 52 週新高", screen.NewHighDays))
	}
	if !slices.Contains(skipped, "L") {
		screen.MinRSScore = ptr(intOr(in.MinRSScore, canslimDefaultRSScore))
		criteria = append(criteria, fmt.Sprintf("L 相對強度 ≥ %d", *screen.MinRSScore))
	}
	if in.MinAvgTradeValue != nil {
		criteria = append(criteria, fmt.Sprintf("S 近 20 日平均成交 ≥ %d 百萬元", *in.MinAvgTradeValue))
	}
	if in.MinForeignStreak != nil {
		criteria = append(criteria, fmt.Sprintf("I 外資連買 ≥ %d 天", *in.MinForeignStreak))
	}
	if in.MinTrustStreak != nil {
		criteria = append(criteria, fmt.Sprintf("I 投信連買 ≥ %d 天", *in.MinTrustStreak))
	}
	return screen, criteria
}

// floatOr 回傳 value 指向的值;nil 時回 fallback。
func floatOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

// intOr 回傳 value;0(沒有提供)時回 fallback。
func intOr(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// marketDirection 讀最新的加權指數趨勢,判斷收盤是否在 60 日均線之上。查詢失敗只記錄,M 標成無法判斷,
// 不讓大盤資料的暫時問題擋掉選股結果。
func (ts *canslimToolset) marketDirection(ctx context.Context) CanslimMarket {
	envelope, err := ts.regime.MarketRegime(ctx, "")
	if err != nil {
		ts.logf("工具 screen_canslim 查詢大盤趨勢失敗:%v", err)
		return CanslimMarket{}
	}
	market := CanslimMarket{DataAsOf: &envelope.DataAsOf, Close: &envelope.Close, Alignment: &envelope.Alignment}
	for _, ma := range envelope.MovingAverages {
		if ma.Days == canslimMarketMovingAverage {
			market.MA60 = ma.Value
			market.Above = ma.Above
		}
	}
	return market
}

// canslimSummary 組出給模型閱讀的摘要:套用的條件、M 的判斷、符合檔數與前幾檔的成長指標。
func canslimSummary(out CanslimOutput) string {
	var b summaryBuilder
	b.printf("CANSLIM 條件:%s", strings.Join(out.Criteria, "、"))
	if len(out.Skipped) > 0 {
		b.printf("(未篩:%s)", strings.Join(out.Skipped, "、"))
	}
	b.printf("。\n%s", canslimMarketText(out.Market))
	if len(out.Stocks) == 0 {
		b.printf("\n沒有符合的股票。可調低門檻(A 的「連 3 年每年 EPS 成長」在台股最嚴格),或用 skip_letters 拿掉 N 再看。")
	} else {
		b.printf("\n符合 %d 檔(依 %s 由高到低,列前 10 檔):", len(out.Stocks), out.SortBy)
		shown := out.Stocks[:min(len(out.Stocks), 10)]
		var lines []string
		for _, stock := range shown {
			lines = append(lines, fmt.Sprintf("%s %s(單季 EPS 年增 %s、近 3 年最低年增 %s、ROE %s、RS %s、52 週新高 %s、外資%s、投信%s)",
				stock.StockSymbol, stock.Name, displayPercent(stock.EPSYoYPercent), displayPercent(stock.AnnualEPSGrowthPercent),
				displayPercent(stock.ReturnOnEquity), displayInt(stock.RSScore), displayString(stock.NewHighDate),
				streakDisplay(stock.ForeignStreak), streakDisplay(stock.TrustStreak)))
		}
		b.printf("\n%s", strings.Join(lines, "\n"))
	}
	b.printf("\n%s。CANSLIM 是篩選框架,過去回測不保證未來報酬。免責聲明:%s", canslimBacktestNote, AnalysisDisclaimer)
	return b.String()
}

// canslimMarketText 描述 M:收盤在 60 日均線之上才符合歐尼爾「順著大盤方向」的原則。
func canslimMarketText(m CanslimMarket) string {
	if m.Above == nil || m.MA60 == nil || m.Close == nil {
		return "M 大盤方向:無法取得加權指數 60 日均線,請另以 get_market_regime 確認。"
	}
	if *m.Above {
		return fmt.Sprintf("M 大盤方向(只供參考):加權指數 %s 收盤 %.2f 在 60 日均線 %.2f 之上,符合順勢進場。",
			displayString(m.DataAsOf), *m.Close, *m.MA60)
	}
	return fmt.Sprintf("M 大盤方向(只供參考):加權指數 %s 收盤 %.2f 跌破 60 日均線 %.2f,歐尼爾的原則是大盤轉弱時不新買進;"+
		"但台股回測以這條濾網空手,報酬反而變差。", displayString(m.DataAsOf), *m.Close, *m.MA60)
}
