// Package stock 的 get_relative_strength_history 工具:個股近 N 個交易日的相對強度(RS)走勢。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/relative-strength:每個交易日都以
// 上市櫃全體計分(與 get_relative_strength_ranking 同一算法),附期間高低點、分數變化與穿越
// 50/80 門檻的轉折。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RelativeStrengthPoint 是單一交易日的相對強度。
type RelativeStrengthPoint struct {
	Date           string  `json:"date"`
	RSScore        int     `json:"rs_score"`
	Rank           int     `json:"rank"`
	Universe       int     `json:"universe"`
	WeightedReturn float64 `json:"weighted_return"`
}

// RelativeStrengthCrossing 是分數穿越門檻的轉折;Direction 為 up 或 down。
type RelativeStrengthCrossing struct {
	Date      string `json:"date"`
	Threshold int    `json:"threshold"`
	Direction string `json:"direction"`
	FromScore int    `json:"from_score"`
	ToScore   int    `json:"to_score"`
}

// RelativeStrengthHistoryEnvelope 是 /stocks/{symbol}/relative-strength 的完整回應;
// 期間內未計分時摘要欄位為 nil、清單為空。
type RelativeStrengthHistoryEnvelope struct {
	StockSymbol string                     `json:"stock_symbol"`
	DataAsOf    *string                    `json:"data_as_of"`
	Days        int                        `json:"days"`
	LatestScore *int                       `json:"latest_score"`
	HighScore   *int                       `json:"high_score"`
	HighDate    *string                    `json:"high_date"`
	LowScore    *int                       `json:"low_score"`
	LowDate     *string                    `json:"low_date"`
	ScoreChange *int                       `json:"score_change"`
	Crossings   []RelativeStrengthCrossing `json:"crossings"`
	Items       []RelativeStrengthPoint    `json:"items"`
}

// RelativeStrengthHistoryQuerier 是 get_relative_strength_history 對資料來源的需求介面;只有 *APIClient 實作。
type RelativeStrengthHistoryQuerier interface {
	RelativeStrengthHistory(ctx context.Context, symbol, date string, days int) (*RelativeStrengthHistoryEnvelope, error)
}

// RelativeStrengthHistory 查詢個股相對強度歷史;404 代表股票代號不存在。
func (c *APIClient) RelativeStrengthHistory(ctx context.Context, symbol, date string, days int) (*RelativeStrengthHistoryEnvelope, error) {
	values := url.Values{"days": []string{fmt.Sprint(days)}}
	if date != "" {
		values.Set("date", date)
	}
	var body RelativeStrengthHistoryEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/relative-strength?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// RelativeStrengthHistoryInput 是 get_relative_strength_history 的輸入。
type RelativeStrengthHistoryInput struct {
	Symbol string `json:"symbol"`
	Date   string `json:"date,omitempty"`
	Days   int    `json:"days,omitempty"`
}

// RelativeStrengthHistoryOutput 是相對強度歷史 envelope 加上 MCP 分析中繼資料。
type RelativeStrengthHistoryOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*RelativeStrengthHistoryEnvelope
}

// relativeStrengthHistorySchema 描述 get_relative_strength_history 的輸入形狀。
func relativeStrengthHistorySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "最後一個交易日(格式 YYYY-MM-DD,選填);未提供為今天,尚未算好的交易日自動略過",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(120.0),
				Default:     []byte("60"),
				Description: "往回幾個交易日(預設 60,範圍 1 至 120)",
			},
		},
		Required: []string{"symbol"},
	}
}

// relativeStrengthHistoryToolset 綁定相對強度歷史資料來源與記錄函式。
type relativeStrengthHistoryToolset struct {
	rs   RelativeStrengthHistoryQuerier
	logf func(string, ...any)
}

// relativeStrengthHistory 執行 get_relative_strength_history。
func (ts *relativeStrengthHistoryToolset) relativeStrengthHistory(ctx context.Context, _ *mcp.CallToolRequest, in RelativeStrengthHistoryInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 60, 1, 120)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 120 之間")
	}
	envelope, err := ts.rs.RelativeStrengthHistory(ctx, symbol, date, days)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_relative_strength_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(relativeStrengthHistorySummary(envelope)), RelativeStrengthHistoryOutput{
		DataKind: "relative_strength_history", IsRealtime: false, Disclaimer: AnalysisDisclaimer, RelativeStrengthHistoryEnvelope: envelope,
	}, nil
}

// relativeStrengthHistoryMaxLines 是摘要逐日列出的上限;完整資料在 structuredContent。
const relativeStrengthHistoryMaxLines = 20

// relativeStrengthHistorySummary 組出給模型閱讀的文字摘要:最新分數、期間高低點、轉折與逐日分數。
func relativeStrengthHistorySummary(e *RelativeStrengthHistoryEnvelope) string {
	var b summaryBuilder
	if e.DataAsOf == nil || len(e.Items) == 0 {
		b.printf("%s 近 %d 個交易日沒有相對強度分數(上市未滿 240 個交易日、暫停交易或非上市櫃)。", e.StockSymbol, e.Days)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	latest := e.Items[0]
	b.printf("%s 近 %d 個交易日的相對強度(分數 1–99 為加權報酬 0.4×近 60 日+0.3×近 120 日+0.3×近 240 日在上市櫃全體的百分位,99 最強):"+
		"%s 為 RS %d(第 %d 名/%d 檔)。期間最高 %s(%s)、最低 %s(%s),期間變化 %s 分。",
		e.StockSymbol, e.Days, latest.Date, latest.RSScore, latest.Rank, latest.Universe,
		displayInt(e.HighScore), displayString(e.HighDate), displayInt(e.LowScore), displayString(e.LowDate), signedInt(e.ScoreChange))
	if len(e.Crossings) == 0 {
		b.printf("\n期間內沒有穿越 50 或 80 分。")
	} else {
		b.printf("\n門檻轉折(由新到舊):")
		for _, c := range e.Crossings {
			direction := "升破"
			if c.Direction == "down" {
				direction = "跌破"
			}
			b.printf("\n- %s %s %d 分(%d → %d)", c.Date, direction, c.Threshold, c.FromScore, c.ToScore)
		}
	}
	b.printf("\n逐日分數(由新到舊):")
	for i, item := range e.Items {
		if i == relativeStrengthHistoryMaxLines {
			b.printf("\n另有 %d 個交易日未列出,完整資料見 structuredContent。", len(e.Items)-i)
			break
		}
		b.printf("\n- %s RS %d(第 %d 名)、加權 %s%%", item.Date, item.RSScore, item.Rank, formatFloat(item.WeightedReturn))
	}
	b.printf("\n過去表現不代表未來結果。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// signedInt 把整數加上正負號;nil 回「無」。
func signedInt(v *int) string {
	if v == nil {
		return "無"
	}
	return fmt.Sprintf("%+d", *v)
}
