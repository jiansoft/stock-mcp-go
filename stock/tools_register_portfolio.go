package stock

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addPortfolioTools 註冊個人持股（總覽、歷史、籌碼、填息、ETF 穿透、再平衡與目標配置版本、股利預估）、
// ETF 與 CAGR 的工具；由 [AddTools] 呼叫，資料來源有實作對應介面才註冊。
func addPortfolioTools(server *mcp.Server, q Querier, logf func(format string, args ...any)) {
	if portfolio, ok := q.(PortfolioQuerier); ok {
		pts := &portfolioToolset{portfolio: portfolio, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_portfolio_summary",
			Description: "查詢個人持股市值、成本、損益、已領股利、產業配置與財務體質警示。",
			InputSchema: portfolioSummarySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, pts.portfolioSummary)
	}

	if history, ok := q.(PortfolioHistoryQuerier); ok {
		hts := &portfolioHistoryToolset{history: history, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_portfolio_history",
			Description: "查詢個人持股歷史時間加權績效、風險及基準比較；股價報酬不含現金股利。",
			InputSchema: portfolioHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.portfolioHistory)
	}

	if hq, ok := q.(HoldingChipQuerier); ok {
		cts := &holdingChipToolset{hq: hq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_holding_chip",
			Description: "查詢個人持股指定交易日的法人、融資融券與主力籌碼日報。",
			InputSchema: holdingChipSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, cts.holdingChip)
	}

	if fq, ok := q.(HoldingDividendFillQuerier); ok {
		fts := &holdingDividendFillToolset{fq: fq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_holding_dividend_fill",
			Description: "查詢目前持股（含 ETF）近期各次除權息進度與歷史填息天數統計。",
			InputSchema: holdingDividendFillSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.holdingDividendFill)
	}

	if eq, ok := q.(EtfQuerier); ok {
		ets := &etfToolset{eq: eq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "screen_etfs",
			Description: "依分類、地區、題材、配息、市場或名稱篩選 ETF，並依規模、折溢價、殖利率或報酬排序。",
			InputSchema: screenEtfsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ets.screenEtfs)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_etf_detail",
			Description: "查詢 ETF 費率、規模、追蹤指數、折溢價、含息績效、風險、持股配置與近期淨值。",
			InputSchema: etfDetailSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ets.etfDetail)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "compare_etfs",
			Description: "比較 2–4 檔 ETF 的費率、規模、折溢價、配息、績效、風險與前十大持股重疊；重疊只涵蓋前十大。",
			InputSchema: compareEtfsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ets.compareEtfs)
	}

	if hq, ok := q.(EtfHoldingHistoryQuerier); ok {
		hts := &etfHoldingChangesToolset{hq: hq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{Name: "get_etf_holding_history", Description: "直接列出 ETF 最近 1–24 期各期前十大成分股與權重；期別依實際採集日期，並非固定季度。", InputSchema: etfHoldingChangesSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, hts.etfHoldingHistory)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_etf_holding_changes",
			Description: "查詢 ETF 最近 1–24 期前十大持股的新進、剔除與權重變化；直接列各期持股請用 get_etf_holding_history。",
			InputSchema: etfHoldingChangesSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.etfHoldingChanges)
	}

	if eq, ok := q.(PortfolioEtfExposureQuerier); ok {
		ets := &portfolioEtfExposureToolset{eq: eq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_portfolio_etf_exposure",
			Description: "查詢個人持股 ETF 前十大成分股穿透曝險，合併直接持股；附未涵蓋金額，非完整持股穿透。",
			InputSchema: portfolioEtfExposureSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ets.portfolioEtfExposure)
	}

	if pr, ok := q.(PortfolioRebalanceQuerier); ok {
		prts := &portfolioRebalanceToolset{pr: pr, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_portfolio_rebalance",
			Description: "試算持股再平衡建議：依設定之目標配置與目前持股試算建議調整股數、預估金額與現金流。僅供參考，絕不下單交易。",
			InputSchema: portfolioRebalanceSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, prts.getPortfolioRebalance)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "set_portfolio_targets",
			Description: "設定投資組合目標配置比例：支援個股、產業或 ETF 穿透維度，各項比例之和不得超過 100%。僅作為個人試算基準，非自動交易委託。",
			InputSchema: setPortfolioTargetsSchema(),
		}, prts.setPortfolioTargets)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_portfolio_targets",
			Description: "查詢已設定之投資組合目標配置比例與現金保留比例。",
			InputSchema: getPortfolioTargetsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, prts.getPortfolioTargets)
	}

	if rq, ok := q.(PortfolioTargetRevisionQuerier); ok {
		rts := &portfolioTargetRevisionToolset{rq: rq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "list_portfolio_target_revisions",
			Description: "列出某維度目標配置的歷史版本(每次設定前的整組,最多 20 版),附各版現金比例;用來找出要還原的版本。",
			InputSchema: listTargetRevisionsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, rts.listTargetRevisions)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "restore_portfolio_target_revision",
			Description: "把目標配置還原成某個歷史版本;還原前的設定會另存成一版,可再還原回去。僅作為個人試算基準,非自動交易委託。",
			InputSchema: restoreTargetRevisionSchema(),
		}, rts.restoreTargetRevision)
	}

	if forecast, ok := q.(DividendForecastQuerier); ok {
		fts := &dividendForecastToolset{forecast: forecast, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_dividend_forecast",
			Description: "預估個人持股未來每月現金股利；區分已公告與歷史推估，推估非保證。",
			InputSchema: dividendForecastSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.dividendForecast)
	}

	if cagr, ok := q.(CagrQuerier); ok {
		cgs := &cagrToolset{cagr: cagr, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_cagr_ranking",
			Description: "查詢全市場多期間 CAGR 排行；可選股價、含股利或股利再投入口徑。",
			InputSchema: cagrRankingSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, cgs.cagrRanking)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_stock_cagr",
			Description: "查詢指定股票 M3、M6、Y1、Y1H(一年半)、Y2、Y3、Y5、Y7、Y10 各期間的年化報酬率與總報酬(固定金額回測,可選報酬口徑)。",
			InputSchema: stockCagrSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, cgs.stockCagr)
	}
}
