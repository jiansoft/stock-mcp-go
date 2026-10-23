// Package stock 的 get_etf_holding_changes 工具:ETF 近幾期的前十大持股與期間變化。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/etfs/{symbol}/holdings/history:每週六採集 Yahoo ETF 頁,
// 收到新一期前十大持股才新增一期(約每月一期);每期附上與前一期相比的新進、剔除與權重變化。
package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// etfHoldingMovesListed 是摘要每期最多列出的權重變化檔數;完整清單在結構化輸出。
const etfHoldingMovesListed = 5

// EtfHoldingWeightItem 是新進或剔除的一檔持股;新進為這一期比例、剔除為前一期比例(%)。
type EtfHoldingWeightItem struct {
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
}

// EtfHoldingMoveItem 是兩期都在前十大、比例有變的一檔持股。
type EtfHoldingMoveItem struct {
	Label  string  `json:"label"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

// EtfHoldingChanges 是這一期與前一期的差異。
type EtfHoldingChanges struct {
	PreviousAsOf string                 `json:"previous_as_of"`
	Added        []EtfHoldingWeightItem `json:"added"`
	Removed      []EtfHoldingWeightItem `json:"removed"`
	Moved        []EtfHoldingMoveItem   `json:"moved"`
}

// EtfHoldingPeriodItem 是一期的前十大持股;沒有更早一期的紀錄時 Changes 為 nil。
type EtfHoldingPeriodItem struct {
	AsOf     string             `json:"as_of"`
	Holdings []EtfHoldingItem   `json:"holdings"`
	Changes  *EtfHoldingChanges `json:"changes"`
}

// EtfHoldingHistoryEnvelope 是 /api/v1/etfs/{symbol}/holdings/history 的回應。
type EtfHoldingHistoryEnvelope struct {
	StockSymbol string                 `json:"stock_symbol"`
	Name        string                 `json:"name"`
	Periods     []EtfHoldingPeriodItem `json:"periods"`
}

// EtfHoldingHistoryQuerier 是 get_etf_holding_changes 對資料來源的需求介面;只有 *APIClient 實作。
type EtfHoldingHistoryQuerier interface {
	// EtfHoldingHistory 查無這檔 ETF 時回傳 nil, nil。
	EtfHoldingHistory(ctx context.Context, symbol string, periods int) (*EtfHoldingHistoryEnvelope, error)
}

// EtfHoldingHistory 查詢 ETF 持股歷史;periods 為 0 代表用 API 預設(6 期);404 代表不是上市櫃 ETF。
func (c *APIClient) EtfHoldingHistory(ctx context.Context, symbol string, periods int) (*EtfHoldingHistoryEnvelope, error) {
	path := "/api/v1/etfs/" + url.PathEscape(symbol) + "/holdings/history"
	if periods > 0 {
		path += "?" + url.Values{"periods": {fmt.Sprint(periods)}}.Encode()
	}
	var body EtfHoldingHistoryEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil || !found {
		return nil, err
	}
	if body.Periods == nil {
		body.Periods = []EtfHoldingPeriodItem{}
	}
	for i := range body.Periods {
		period := &body.Periods[i]
		if period.Holdings == nil {
			period.Holdings = []EtfHoldingItem{}
		}
		if c := period.Changes; c != nil {
			if c.Added == nil {
				c.Added = []EtfHoldingWeightItem{}
			}
			if c.Removed == nil {
				c.Removed = []EtfHoldingWeightItem{}
			}
			if c.Moved == nil {
				c.Moved = []EtfHoldingMoveItem{}
			}
		}
	}
	return &body, nil
}

// EtfHoldingChangesInput 是 get_etf_holding_changes 的輸入。
type EtfHoldingChangesInput struct {
	Symbol  string `json:"symbol"`
	Periods int    `json:"periods,omitempty"`
}

// EtfHoldingChangesOutput 是 ETF 持股歷史加上 MCP 分析中繼資料。
type EtfHoldingChangesOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*EtfHoldingHistoryEnvelope
}

// etfHoldingChangesSchema 描述 get_etf_holding_changes 的輸入形狀;新參數不帶 schema 預設值。
func etfHoldingChangesSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"symbol"},
		Properties: map[string]*jsonschema.Schema{
			"symbol":  {Type: "string", MinLength: ptr(1), MaxLength: ptr(12), Description: "ETF 代號,例如 0056、00878"},
			"periods": {Type: "integer", Minimum: ptr(1.0), Maximum: ptr(24.0), Description: "回傳最近幾期(選填,1 至 24;未提供為 6,約每月一期)"},
		},
	}
}

// etfHoldingChangesToolset 綁定 ETF 持股歷史資料來源與記錄函式。
type etfHoldingChangesToolset struct {
	hq   EtfHoldingHistoryQuerier
	logf func(string, ...any)
}

// etfHoldingChanges 執行 get_etf_holding_changes。
func (ts *etfHoldingChangesToolset) etfHoldingChanges(ctx context.Context, _ *mcp.CallToolRequest, in EtfHoldingChangesInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	if in.Periods != 0 && (in.Periods < 1 || in.Periods > 24) {
		return nil, nil, fmt.Errorf("參數 periods 必須介於 1 到 24 之間")
	}
	envelope, err := ts.hq.EtfHoldingHistory(ctx, symbol, in.Periods)
	if err != nil {
		ts.logf("工具 get_etf_holding_changes 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if envelope == nil {
		return nil, nil, fmt.Errorf("查無 ETF %s;可用 screen_etfs 以名稱或關鍵字找代號", symbol)
	}
	return textResult(etfHoldingChangesSummary(envelope)), EtfHoldingChangesOutput{
		DataKind: "etf_holding_changes", IsRealtime: false, Disclaimer: AnalysisDisclaimer, EtfHoldingHistoryEnvelope: envelope,
	}, nil
}

// weightLabels 把新進或剔除的持股寫成「名稱 比例%」。
func weightLabels(rows []EtfHoldingWeightItem) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s %s%%", row.Label, formatFloat(row.Weight)))
	}
	return strings.Join(parts, "、")
}

// etfHoldingChangesSummary 組出給模型閱讀的摘要:最新一期前十大,再逐期列出新進、剔除與權重變化最大的幾檔。
func etfHoldingChangesSummary(e *EtfHoldingHistoryEnvelope) string {
	var b summaryBuilder
	if len(e.Periods) == 0 {
		b.printf("%s %s 沒有前十大持股紀錄(反向、期貨型 ETF 通常沒有)。", e.StockSymbol, e.Name)
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	latest := e.Periods[0]
	holdings := make([]string, 0, len(latest.Holdings))
	for _, h := range latest.Holdings {
		label := h.Name
		if h.Symbol != "" {
			label = h.Symbol + " " + h.Name
		}
		holdings = append(holdings, fmt.Sprintf("%s %s%%", label, formatFloat(h.Weight)))
	}
	b.printf("%s %s 最新一期(%s)前十大持股:%s。", e.StockSymbol, e.Name, latest.AsOf, strings.Join(holdings, "、"))
	for _, period := range e.Periods {
		c := period.Changes
		if c == nil {
			b.printf("\n%s:沒有更早一期的紀錄可比較。", period.AsOf)
			continue
		}
		var parts []string
		if len(c.Added) > 0 {
			parts = append(parts, "新進 "+weightLabels(c.Added))
		}
		if len(c.Removed) > 0 {
			parts = append(parts, "剔除 "+weightLabels(c.Removed))
		}
		if len(c.Moved) > 0 {
			moves := make([]string, 0, etfHoldingMovesListed)
			for _, m := range c.Moved[:min(len(c.Moved), etfHoldingMovesListed)] {
				moves = append(moves, fmt.Sprintf("%s %s%%→%s%%", m.Label, formatFloat(m.Before), formatFloat(m.After)))
			}
			text := "權重變化 " + strings.Join(moves, "、")
			if hidden := len(c.Moved) - len(moves); hidden > 0 {
				text += fmt.Sprintf(" 等 %d 檔", len(c.Moved))
			}
			parts = append(parts, text)
		}
		if len(parts) == 0 {
			parts = append(parts, "前十大與權重都沒有變")
		}
		b.printf("\n%s(相對 %s):%s。", period.AsOf, c.PreviousAsOf, strings.Join(parts, ";"))
	}
	b.printf("\n只涵蓋前十大持股,十大以外的成分股變化看不到。免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
