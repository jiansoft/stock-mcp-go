package stock

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 具名策略:存在 stock_rust 的回測條件,回測類工具以 preset 參數引用。

// StrategyPreset 是一個具名策略。
type StrategyPreset struct {
	Name        string `json:"name"`
	Query       string `json:"query"`
	Description string `json:"description"`
	Alert       bool   `json:"alert"`
}

// StrategyPresetsEnvelope 是 /strategy-presets endpoint 的完整回應。
type StrategyPresetsEnvelope struct {
	Presets []StrategyPreset `json:"presets"`
}

// StrategyPresetsQuerier 是 list_strategy_presets 對資料來源的需求介面;只有 *APIClient 實作。
type StrategyPresetsQuerier interface {
	StrategyPresets(ctx context.Context) (*StrategyPresetsEnvelope, error)
}

// StrategyPresets 查詢全部具名策略。
func (c *APIClient) StrategyPresets(ctx context.Context) (*StrategyPresetsEnvelope, error) {
	var body StrategyPresetsEnvelope
	if err := c.get(ctx, "/api/v1/strategy-presets", &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// StrategyPresetsOutput 是策略清單加上 MCP 中繼資料。
type StrategyPresetsOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	*StrategyPresetsEnvelope
}

// strategyPresetsSchema 沒有參數。
func strategyPresetsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
}

// strategyPresetsToolset 綁定策略清單資料來源與記錄函式。
type strategyPresetsToolset struct {
	pq   StrategyPresetsQuerier
	logf func(string, ...any)
}

// strategyPresets 執行 list_strategy_presets。
func (ts *strategyPresetsToolset) strategyPresets(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	envelope, err := ts.pq.StrategyPresets(ctx)
	if err != nil {
		ts.logf("工具 list_strategy_presets 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(strategyPresetsSummary(envelope)), StrategyPresetsOutput{
		DataKind: "strategy_presets", IsRealtime: false, StrategyPresetsEnvelope: envelope,
	}, nil
}

// strategyPresetsSummary 列出每個策略的名稱、說明、條件與是否發換股通知。
func strategyPresetsSummary(e *StrategyPresetsEnvelope) string {
	var b summaryBuilder
	if len(e.Presets) == 0 {
		b.printf("目前沒有具名策略(由 stock_rust 手動回補管理介面的 /api/strategy-presets 新增)。")
		return b.String()
	}
	b.printf("具名策略 %d 個(回測類工具以 preset=名稱 引用,另外指定的參數會覆蓋存的值):", len(e.Presets))
	for _, preset := range e.Presets {
		alert := ""
		if preset.Alert {
			alert = "【每個交易日 22:10 發換股通知】"
		}
		description := ""
		if preset.Description != "" {
			description = ":" + preset.Description
		}
		b.printf("\n- %s%s%s\n  條件 %s", preset.Name, alert, description, preset.Query)
	}
	return b.String()
}
