// get_signal_performance／get_stock_signal_history 的輸入 schema。

package stock

import "github.com/google/jsonschema-go/jsonschema"

// signalFilterSchemas 回傳 kind 與 indicator 兩個篩選參數的 schema。
func signalFilterSchemas(target string) (*jsonschema.Schema, *jsonschema.Schema) {
	kinds := make([]any, 0, len(signalKinds))
	for _, kind := range signalKinds {
		kinds = append(kinds, kind.name)
	}
	indicators := make([]any, 0, len(signalIndicators))
	for _, indicator := range signalIndicators {
		indicators = append(indicators, indicator.name)
	}
	kind := &jsonschema.Schema{
		Type: "string",
		Enum: kinds,
		Description: "只" + target + "這種訊號(選填):golden_cross 黃金交叉、death_cross 死亡交叉、cross_above 收盤站上均線或 RS 向上穿越、" +
			"cross_below 收盤跌破均線或 RS 向下穿越、overbought 超買、oversold 超賣、new_high 52 週新高、new_low 52 週新低、breakout 區間整理後帶量突破",
	}
	indicator := &jsonschema.Schema{
		Type: "string",
		Enum: indicators,
		Description: "只" + target + "這個來源的訊號(選填):ma5_ma20、ma20_ma60(均線交叉)、kd、macd、" +
			"close_ma20/close_ma60/close_ma240(收盤站上或跌破月/季/年線)、rsi14、52_week(52 週新高新低)、" +
			"rs80/rs50(相對強度分數比前一個交易日站上或跌破 80/50 分)、" +
			"range_breakout(區間突破:前 30 日整理幅度 20% 內,收盤帶前 20 日均量 1.5 倍以上突破整理高點,且離突破價不超過 5%)",
	}
	return kind, indicator
}

// signalPerformanceSchema 描述 get_signal_performance 的輸入形狀。
func signalPerformanceSchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("統計")
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計起日(格式 YYYY-MM-DD,選填);未提供為迄日往前 364 天",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "統計迄日(格式 YYYY-MM-DD,選填);未提供為最新交易日。起訖最長 3,653 天,超過 1,096 天時必須指定 indicator",
			},
			"market":    listedOTCMarketSchema(),
			"kind":      kind,
			"indicator": indicator,
			"industry": {
				Type:        "string",
				MaxLength:   ptr(20),
				Description: "只統計這個產業的訊號(選填),需為完整產業名稱,例如「半導體業」;大盤基準不受影響",
			},
			"min_rs_score":       rsScoreBoundSchema("只統計訊號當天相對強度(RS)分數不低於此值的訊號(選填,1 至 99)"),
			"max_rs_score":       rsScoreBoundSchema("只統計訊號當天相對強度(RS)分數不高於此值的訊號(選填,1 至 99)"),
			"min_foreign_streak": streakBoundSchema("外資(含外資自營商)"),
			"min_trust_streak":   streakBoundSchema("投信"),
			"group_by":           groupBySchema("訊號"),
		},
	}
}

// groupBySchema 描述依年度拆分的參數。
func groupBySchema(target string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Enum: []any{"year"},
		Description: "選填;設為 year 時除了整段期間,再依" + target + "日所在年度拆分統計(各年度的次數、報酬與大盤基準)," +
			"用來看在多頭、空頭年份是否都有效;未提供為不拆分",
	}
}

// rsScoreBoundSchema 描述相對強度分數上下限參數:1 至 99 的整數。
func rsScoreBoundSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:    "integer",
		Minimum: ptr(1.0),
		Maximum: ptr(99.0),
		Description: description + ";分數以上市櫃普通股(不含 ETF、特別股)計算,99 最強。有設定時,訊號當天沒有分數" +
			"(ETF、特別股、上市未滿 240 個交易日)的訊號不列入;大盤基準不受影響",
	}
}

// maxStreakDays 是法人連續買超天數條件的上限,與 Data API 一致(連續天數入庫時預先計算、不限回看長度)。
const maxStreakDays = 60

// streakBoundSchema 描述法人連續買超天數下限參數:1 至 60 的整數。
func streakBoundSchema(investor string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:    "integer",
		Minimum: ptr(1.0),
		Maximum: ptr(float64(maxStreakDays)),
		Description: "只統計訊號當天" + investor + "已連續買超至少這麼多天的訊號(選填,1 至 60);" +
			"連續天數與 get_institutional_flow_ranking 同一算法,籌碼資料從 2019 年起,之前的訊號不列入;" +
			"外資與投信都設時需同時符合,大盤基準不受影響",
	}
}

// stockSignalHistorySchema 描述 get_stock_signal_history 的輸入形狀。
func stockSignalHistorySchema() *jsonschema.Schema {
	kind, indicator := signalFilterSchemas("列出")
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "迄日(格式 YYYY-MM-DD,選填);未提供為今天",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(730.0),
				Default:     []byte("180"),
				Description: "往前幾個日曆天(含迄日,預設 180,範圍 1 至 730)",
			},
			"kind":      kind,
			"indicator": indicator,
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Default:     []byte("30"),
				Description: "最多列出幾筆(預設 30,範圍 1 至 200),由新到舊",
			},
		},
		Required: []string{"symbol"},
	}
}
