// Package stock 的 get_holder_levels 工具:查詢單一股票單週的集保股權分散全部持股分級。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/holder-levels;
// 集保開放資料只有最新一週,分級資料自 2026-10-22 起逐週累積,更早的週只有
// get_chip_data 的千張與 400 張摘要。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrHolderLevelsNotFound 表示股票不存在,或指定期間沒有分級資料(Data API 回 404)。
var ErrHolderLevelsNotFound = errors.New("holder levels not found")

// HolderLevel 是單一持股分級的人數、股數與比例。
// Level 1～15 依持股數由小到大,16 是差異數調整,17 是合計。
type HolderLevel struct {
	Level   int64    `json:"level"`
	Label   string   `json:"label"`
	Holders int64    `json:"holders"`
	Shares  int64    `json:"shares"`
	Percent *float64 `json:"percent"`
}

// HolderLevelsEnvelope 是 holder-levels endpoint 的完整回應。
type HolderLevelsEnvelope struct {
	StockSymbol string        `json:"stock_symbol"`
	Date        string        `json:"date"`
	Levels      []HolderLevel `json:"levels"`
	Adjustment  *HolderLevel  `json:"adjustment"`
	Total       *HolderLevel  `json:"total"`
}

// HolderLevels 查詢 date(含)以前最近一週的分級;date 為空字串時取最新一週。
func (c *APIClient) HolderLevels(ctx context.Context, symbol, date string) (*HolderLevelsEnvelope, error) {
	path := "/api/v1/stocks/" + url.PathEscape(symbol) + "/holder-levels"
	if date != "" {
		path += "?" + url.Values{"date": []string{date}}.Encode()
	}
	var body HolderLevelsEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrHolderLevelsNotFound
	}
	if body.Levels == nil {
		body.Levels = []HolderLevel{}
	}
	return &body, nil
}

// HolderLevelsInput 是 get_holder_levels 的輸入。
type HolderLevelsInput struct {
	Symbol string `json:"symbol"`
	Date   string `json:"date,omitempty"`
}

// HolderLevelsOutput 是分級 envelope 加上 MCP 分析中繼資料。
type HolderLevelsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*HolderLevelsEnvelope
}

// holderLevelsSchema 描述 get_holder_levels 的輸入形狀。
func holderLevelsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "資料日期(YYYY-MM-DD,選填;取該日以前最近一週,預設最新一週;分級資料自 2026-10-22 起)",
			},
		},
		Required: []string{"symbol"},
	}
}

// holderLevels 執行 get_holder_levels。
func (ts *chipToolset) holderLevels(ctx context.Context, _ *mcp.CallToolRequest, in HolderLevelsInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	if _, err := parseDateArg(in.Date, "date"); err != nil {
		return nil, nil, err
	}
	envelope, err := ts.chips.HolderLevels(ctx, symbol, in.Date)
	if err != nil {
		if errors.Is(err, ErrHolderLevelsNotFound) {
			return nil, nil, fmt.Errorf("找不到 %s 的集保持股分級資料(股票不存在,或該期間沒有資料;分級資料自 2026-10-22 起逐週累積)", in.Symbol)
		}
		ts.logf("工具 get_holder_levels 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(holderLevelsSummary(envelope)), HolderLevelsOutput{
		DataKind: "holder_levels", IsRealtime: false, Disclaimer: AnalysisDisclaimer, HolderLevelsEnvelope: envelope,
	}, nil
}

// holderLevelsSummary 組出給模型閱讀的文字摘要:每級一段,最後附合計。
func holderLevelsSummary(e *HolderLevelsEnvelope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "股票 %s 集保股權分散 %s:", e.StockSymbol, e.Date)
	parts := make([]string, 0, len(e.Levels))
	for _, level := range e.Levels {
		parts = append(parts, fmt.Sprintf("%s %d 人、%s", level.Label, level.Holders, displayPercent(level.Percent)))
	}
	b.WriteString(strings.Join(parts, ";"))
	b.WriteString("。")
	if e.Total != nil {
		fmt.Fprintf(&b, "合計 %d 人、%d 股。", e.Total.Holders, e.Total.Shares)
	}
	fmt.Fprintf(&b, "\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
