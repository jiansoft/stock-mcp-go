// Package stock 的 get_holding_dividend_fill 工具:持股近期每次除權息的填息狀態。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/portfolio/dividend-fill:目前持股(含 ETF)除權息日在近 N 天內
// 的每次除權息,填息規則與 get_dividend_fill 相同;尚未填息的依距離填息價的缺口由大到小排在前面。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// holdingDividendFillListed 是摘要中未填息、已填息各列出的最多筆數;完整清單在結構化輸出。
const holdingDividendFillListed = 15

// HoldingDividendFillItem 是持股的一次除權息與填息狀態;填息欄位與 DividendFillEvent 相同(JSON 展開)。
// History 是該股近 5 年(含本次)的填息統計,供對照這次填息快慢;同一檔每列相同。
type HoldingDividendFillItem struct {
	StockSymbol string `json:"stock_symbol"`
	Name        string `json:"name"`
	DividendFillEvent
	History DividendFillSummary `json:"history"`
}

// HoldingDividendFillEnvelope 是 portfolio/dividend-fill endpoint 的完整回應。
type HoldingDividendFillEnvelope struct {
	MemberID int64                     `json:"member_id"`
	Days     int                       `json:"days"`
	Summary  DividendFillSummary       `json:"summary"`
	Items    []HoldingDividendFillItem `json:"items"`
}

// HoldingDividendFillQuerier 是 get_holding_dividend_fill 對資料來源的需求介面;只有 *APIClient 實作。
type HoldingDividendFillQuerier interface {
	HoldingDividendFill(ctx context.Context, days int, memberID int64) (*HoldingDividendFillEnvelope, error)
}

// HoldingDividendFill 查詢持股填息狀態;days 為 0 代表用 API 預設(365 天),memberID 為 0 是全部會員合計。
func (c *APIClient) HoldingDividendFill(ctx context.Context, days int, memberID int64) (*HoldingDividendFillEnvelope, error) {
	path := "/api/v1/portfolio/dividend-fill"
	values := url.Values{}
	if days > 0 {
		values.Set("days", fmt.Sprint(days))
	}
	if memberID > 0 {
		values.Set("member_id", fmt.Sprint(memberID))
	}
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	var body HoldingDividendFillEnvelope
	if err := c.get(ctx, path, &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []HoldingDividendFillItem{}
	}
	return &body, nil
}

// HoldingDividendFillInput 是 get_holding_dividend_fill 的輸入。
type HoldingDividendFillInput struct {
	Days     int   `json:"days,omitempty"`
	MemberID int64 `json:"member_id,omitempty"`
}

// HoldingDividendFillOutput 是持股填息 envelope 加上 MCP 分析中繼資料。
type HoldingDividendFillOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*HoldingDividendFillEnvelope
}

// holdingDividendFillSchema 描述 get_holding_dividend_fill 的輸入形狀;新參數不帶 schema 預設值。
func holdingDividendFillSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"days": {
				Type: "integer", Minimum: ptr(30.0), Maximum: ptr(730.0),
				Description: "只看除權息日在今天往前幾天內的事件(選填,30 至 730;未提供為 365)",
			},
			"member_id": {
				Type: "integer", Minimum: ptr(0.0),
				Description: "會員編號(選填);未提供或 0 為全部會員合計",
			},
		},
	}
}

// holdingDividendFillToolset 綁定持股填息資料來源與記錄函式。
type holdingDividendFillToolset struct {
	fq   HoldingDividendFillQuerier
	logf func(string, ...any)
}

// holdingDividendFill 執行 get_holding_dividend_fill。
func (ts *holdingDividendFillToolset) holdingDividendFill(ctx context.Context, _ *mcp.CallToolRequest, in HoldingDividendFillInput) (*mcp.CallToolResult, any, error) {
	if in.Days != 0 && (in.Days < 30 || in.Days > 730) {
		return nil, nil, fmt.Errorf("參數 days 必須介於 30 到 730 之間")
	}
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	envelope, err := ts.fq.HoldingDividendFill(ctx, in.Days, in.MemberID)
	if err != nil {
		ts.logf("工具 get_holding_dividend_fill 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(holdingDividendFillSummary(envelope)), HoldingDividendFillOutput{
		DataKind: "holding_dividend_fill", IsRealtime: false, Disclaimer: AnalysisDisclaimer, HoldingDividendFillEnvelope: envelope,
	}, nil
}

// holdingDividendFillSummary 組出給模型閱讀的文字摘要:統計、未填息(缺口大到小)與最近填息的持股。
func holdingDividendFillSummary(e *HoldingDividendFillEnvelope) string {
	var b summaryBuilder
	s := e.Summary
	scope := "全部會員"
	if e.MemberID > 0 {
		scope = fmt.Sprintf("會員 %d 的", e.MemberID)
	}
	b.printf("%s持股近 %d 天的除權息共 %d 次,已填息 %d 次(已填息者平均 %s 個交易日、中位數 %s)。",
		scope, e.Days, s.Events, s.Filled, displayFloat(s.AverageTradingDaysToFill), displayFloat(s.MedianTradingDaysToFill))
	var unfilled, filled []string
	for _, item := range e.Items {
		if item.Filled {
			filled = append(filled, fmt.Sprintf("%s %s %s 除權息、第 %s 個交易日填息",
				item.StockSymbol, item.Name, item.ExDate, intPtrText(item.TradingDaysToFill)))
			continue
		}
		unfilled = append(unfilled, fmt.Sprintf("%s %s %s 除權息(%s)填息價 %s、最新 %s,還差 %s(已 %d 個交易日%s)",
			item.StockSymbol, item.Name, item.ExDate, fillDividendText(item.DividendFillEvent),
			displayFloat(item.PreExClose), displayFloat(item.LatestClose), displayPercent(item.GapPercent), item.TradingDaysSinceEx,
			fillHistoryText(item.History)))
	}
	writeFillList(&b, "尚未填息", unfilled)
	writeFillList(&b, "已填息", filled)
	b.printf("\n填息價為除權息前一日收盤,價格未還原。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}

// fillHistoryText 是未填息列附的近 5 年填息紀錄;沒有歷史事件時不附。
func fillHistoryText(h DividendFillSummary) string {
	if h.Events == 0 {
		return ""
	}
	if h.AverageTradingDaysToFill == nil {
		return fmt.Sprintf(";近 5 年 %d 次除權息都未填息", h.Events)
	}
	return fmt.Sprintf(";近 5 年 %d 次填息 %d 次、平均 %s 個交易日", h.Events, h.Filled, displayFloat(h.AverageTradingDaysToFill))
}

// writeFillList 寫出一段清單,超過 holdingDividendFillListed 筆時註明省略數。
func writeFillList(b *summaryBuilder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	shown := lines[:min(len(lines), holdingDividendFillListed)]
	b.printf("\n%s(%d 次):%s", title, len(lines), strings.Join(shown, ";"))
	if hidden := len(lines) - len(shown); hidden > 0 {
		b.printf(";另有 %d 次見結構化輸出", hidden)
	}
	b.printf("。")
}

// fillDividendText 顯示現金與股票股利,沒有的那一種不列。
func fillDividendText(e DividendFillEvent) string {
	var parts []string
	if e.CashDividend != nil && *e.CashDividend > 0 {
		parts = append(parts, "現金 "+formatFloat(*e.CashDividend)+" 元")
	}
	if e.StockDividend != nil && *e.StockDividend > 0 {
		parts = append(parts, "股票 "+formatFloat(*e.StockDividend)+" 元")
	}
	return strings.Join(parts, "、")
}

// intPtrText 顯示 *int;nil 顯示「無」。
func intPtrText(v *int) string {
	if v == nil {
		return "無"
	}
	return fmt.Sprint(*v)
}
