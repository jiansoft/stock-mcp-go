package stock

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addBacktestTools 註冊條件選股回測（含偏移、前緣、情境比較、歸因、滾動前推）與具名策略的工具；
// 由 [AddTools] 呼叫，資料來源有實作對應介面才註冊。
func addBacktestTools(server *mcp.Server, q Querier, logf func(format string, args ...any)) {
	if bq, ok := q.(ScreenBacktestQuerier); ok {
		bts := &screenBacktestToolset{bq: bq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_screen_backtest",
			Description: "回測多因子選股策略：定期換股、交易成本、持股權重、停損停利與市場濾網；附逐期及年度績效。歷史非預測。",
			InputSchema: screenBacktestSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, bts.screenBacktest)
	}

	if oq, ok := q.(ScreenBacktestOffsetsQuerier); ok {
		ots := &screenBacktestOffsetsToolset{oq: oq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_screen_backtest_offsets",
			Description: "回測穩健度：比較不同換股日起點的績效分布，可切樣本內外；成本規則同單次回測。",
			InputSchema: screenBacktestOffsetsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ots.screenBacktestOffsets)
	}

	if fq, ok := q.(ScreenBacktestFrontierQuerier); ok {
		fts := &screenBacktestFrontierToolset{fq: fq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_screen_backtest_frontier",
			Description: "回測策略與 0050 核心部位的配置效率前緣，比較報酬、夏普與回撤；支援樣本內外分割。",
			InputSchema: screenBacktestFrontierSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.screenBacktestFrontier)
	}

	if cq, ok := q.(ScreenBacktestCompareQuerier); ok {
		cts := &screenBacktestCompareToolset{cq: cq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "compare_screen_backtests",
			Description: "並排比較 2～4 個回測情境（同一組條件只改成交價模型、滑價、手續費、0050 比例、單檔上限、緩衝或換股日偏移），看成交假設對績效的影響。",
			InputSchema: screenBacktestCompareSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, cts.screenBacktestCompare)
	}

	if aq, ok := q.(ScreenBacktestAttributionQuerier); ok {
		ats := &screenBacktestAttributionToolset{aq: aq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_screen_backtest_attribution",
			Description: "以 Brinson-Fachler 拆解策略超額報酬的產業配置、選股與交互效果；基準為類 0050 組合，未扣成本。",
			InputSchema: screenBacktestAttributionSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ats.screenBacktestAttribution)
	}

	if wq, ok := q.(ScreenBacktestWalkForwardQuerier); ok {
		wts := &screenBacktestWalkForwardToolset{wq: wq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_screen_backtest_walk_forward",
			Description: "滾動前推回測：每年僅用先前訓練資料挑參數，再評估樣本外績效，與固定參數及 0050 比較；運算較慢。",
			InputSchema: screenBacktestWalkForwardSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, wts.screenBacktestWalkForward)
	}

	if pq, ok := q.(StrategyPresetsQuerier); ok {
		pts := &strategyPresetsToolset{pq: pq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "list_strategy_presets",
			Description: "列出具名策略的條件、說明與換股通知設定，供回測或實盤追蹤使用。",
			InputSchema: strategyPresetsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, pts.strategyPresets)
	}

	if eq, ok := q.(StrategyEdgeQuerier); ok {
		ets := &strategyEdgeToolset{q: eq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_strategy_edge",
			Description: "查詢策略是否仍勝過 0050 的最近檢查快取，每月第一個交易日更新；要即時數字請用 get_screen_backtest_offsets。僅供參考，不構成投資建議，亦不會自動下單。",
			InputSchema: strategyEdgeSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ets.strategyEdge)
	}

	if tq, ok := q.(StrategyTrackQuerier); ok {
		sts := &strategyTrackToolset{tq: tq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_strategy_track",
			Description: "查詢具名策略實盤記錄的逐期持股、權重與績效；與事後回測分開。",
			InputSchema: strategyTrackSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, sts.strategyTrack)
	}
}
