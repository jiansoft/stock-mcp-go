package stock

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrStrategyEdgeNotFound 表示尚無每月策略優勢檢查快取。
var ErrStrategyEdgeNotFound = errors.New("strategy edge not found")

// StrategyEdgeWindow 是一段期間的換股日偏移分布；報酬為百分比，勝率為 0–1。
type StrategyEdgeWindow struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Offsets         int      `json:"offsets"`
	MedianReturn    *float64 `json:"median_return"`
	EtfMedianReturn *float64 `json:"etf_median_return"`
	BeatRate        *float64 `json:"beat_rate"`
}

// StrategyEdgeItem 是單一策略最近的檢查；判斷只依近期一段。
type StrategyEdgeItem struct {
	Name    string              `json:"name"`
	Checked string              `json:"checked"`
	Recent  StrategyEdgeWindow  `json:"recent"`
	Full    *StrategyEdgeWindow `json:"full"`
	Verdict string              `json:"verdict"`
}

// StrategyEdgeEnvelope 是 Data API 快取回應，保留實際成交假設與聲明。
type StrategyEdgeEnvelope struct {
	Strategies     []StrategyEdgeItem `json:"strategies"`
	ExecutionPrice string             `json:"execution_price"`
	SlippageBPS    int                `json:"slippage_bps"`
	Disclaimer     string             `json:"disclaimer"`
}

// StrategyEdgeQuerier 是 get_strategy_edge 對資料來源的唯一需求。
type StrategyEdgeQuerier interface {
	StrategyEdge(context.Context) (*StrategyEdgeEnvelope, error)
}

// StrategyEdge 讀取每月快取；不執行回測，404 與服務故障分開處理。
func (c *APIClient) StrategyEdge(ctx context.Context) (*StrategyEdgeEnvelope, error) {
	var body StrategyEdgeEnvelope
	found, err := c.getFound(ctx, "/api/v1/strategy-presets/edge", &body)
	if err != nil {
		return nil, fmt.Errorf("讀取策略優勢:%w", err)
	}
	if !found {
		return nil, ErrStrategyEdgeNotFound
	}
	return &body, nil
}

// StrategyEdgeOutput 在快取回應上揭露 MCP 資料種類與非即時屬性。
type StrategyEdgeOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	*StrategyEdgeEnvelope
}

// strategyEdgeToolset 綁定資料來源與伺服器日誌。
type strategyEdgeToolset struct {
	q    StrategyEdgeQuerier
	logf func(string, ...any)
}

// strategyEdge 只讀取快取並回傳可理解的摘要及原始數字。
func (ts *strategyEdgeToolset) strategyEdge(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	envelope, err := ts.q.StrategyEdge(ctx)
	if errors.Is(err, ErrStrategyEdgeNotFound) {
		return nil, nil, fmt.Errorf("尚無策略優勢檢查紀錄；每月第一個交易日更新，即時查詢請用 get_screen_backtest_offsets")
	}
	if err != nil || envelope == nil {
		ts.logf("工具 get_strategy_edge 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(strategyEdgeSummary(envelope)), StrategyEdgeOutput{DataKind: "strategy_edge", IsRealtime: false, StrategyEdgeEnvelope: envelope}, nil
}

// strategyEdgeSummary 同時列出檢查日期、兩段分布與判斷，避免把快取誤認成即時績效。
func strategyEdgeSummary(e *StrategyEdgeEnvelope) string {
	var b summaryBuilder
	b.printf("策略優勢檢查快取（每月第一個交易日更新；成交價 %s，單邊滑價 %d bps）。", e.ExecutionPrice, e.SlippageBPS)
	window := func(label string, w StrategyEdgeWindow) {
		beat := "無"
		if w.BeatRate != nil {
			beat = formatFloat(*w.BeatRate*100) + "%"
		}
		b.printf("\n  %s %s～%s（%d 個偏移）：扣成本年化中位數 %s，0050 %s，勝 0050 偏移 %s。", label, w.From, w.To, w.Offsets, displayPercent(w.MedianReturn), displayPercent(w.EtfMedianReturn), beat)
	}
	for _, item := range e.Strategies {
		b.printf("\n- %s（%s 檢查）：%s。", item.Name, item.Checked, item.Verdict)
		window("近 24 個月", item.Recent)
		if item.Full != nil {
			window("全期", *item.Full)
		}
	}
	b.printf("\n即時數字請用 get_screen_backtest_offsets。\n%s\n僅供參考，不構成投資建議，亦不會自動下單。", AnalysisDisclaimer)
	return b.String()
}

// strategyEdgeSchema 沒有查詢參數，工具只讀取最近一次快取。
func strategyEdgeSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
}
