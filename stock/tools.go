package stock

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 本檔案定義四個對外提供的 MCP tool(工具):search_stock、
// get_latest_daily_quote、get_price_history、get_stock_profile。每個
// 工具都包含三件事:
//  1. 工具定義(AddTools 裡呼叫 mcp.AddTool):名稱、給 LLM 看的描述文字,
//     以及描述輸入參數形狀的 JSON Schema(回應 MCP 的 tools/list 方法)。
//  2. 輸入驗證(validateLength、normalizeSymbol、rangedLimit、
//     parseDateArg 等輔助函式):對應規格書用 Zod(TypeScript 版)或手寫
//     驗證(Rust 版)做的檢查,在真正呼叫資料庫之前先擋掉不合法的輸入。
//  3. 執行邏輯:呼叫 Querier 介面查資料庫,組成 MCP 回應格式(文字摘要 +
//     structuredContent 結構化資料)。
// ## MCP 規格裡兩種不同層次的「錯誤」,務必分清楚
//
// MCP 協定把「錯誤」分成兩個完全不同的層次:
//
//   - 協定層級錯誤(protocol-level error):例如呼叫了一個根本不存在的
//     工具名稱。這種錯誤由 go-sdk 框架自己處理(對應 JSON-RPC 的 error
//     欄位),本檔案的程式碼不需要處理這一層。
//   - 工具執行層級錯誤(tool-level error):例如查詢的股票代號不存在、
//     或者呼叫端給的參數格式不合法。這種情況在 MCP 協定裡仍然算是
//     「成功」呼叫了工具(有 result,沒有 JSON-RPC error),只是回應
//     裡的 isError 欄位是 true,content 放一段說明錯誤原因的文字,讓
//     呼叫方(通常是 LLM)可以把這段文字當作一般訊息讀懂、自行決定下一步
//     (例如換一個股票代號再試一次),而不必去解析一個協定層級的錯誤碼。
//
// 本檔案採用的 go-sdk(github.com/modelcontextprotocol/go-sdk/mcp)有一個
// 重要的行為:如果本檔案的工具 handler 函式回傳一般的 Go error(不是
// nil),SDK 會自動把這個 error 包裝成上述「isError: true」的工具結果,
// 不需要我們手動組裝 mcp.CallToolResult{IsError: true, ...}。因此下面
// 每個工具函式看到 `return nil, nil, err` 這種寫法,實際的效果就是「回傳
// 一個 MCP 工具執行層級的錯誤」,而不是讓整個 MCP 呼叫失敗。錯誤訊息因此
// 必須是「可以安全回給用戶端」的文字,絕不可包含資料庫主機位址、SQL 原文
// 或 Go 的堆疊資訊。

// Disclaimer 是所有價格資料共用的免責聲明文字,逐字對應規格書要求:任何
// 報價都必須明確標示「非交易所保證即時行情」,避免使用者誤以為這是即時
// 逐筆行情。
const Disclaimer = "本資料為資料庫中最新可取得的日報價或歷史日線資料,非交易所保證即時行情,僅供資訊參考。"

// AnalysisDisclaimer 是分析型資料(月營收、財報、股利等歷史/計算結果)
// 共用的免責聲明,逐字對應計畫 §3.1 的要求:明確告知資料可能延遲、
// 僅供資訊參考,且絕不構成投資建議——這條界線是所有財務資料工具的
// 硬性規範,摘要與 structuredContent 都必須帶上。
const AnalysisDisclaimer = "本資料來自 stock_rust 已蒐集與計算的歷史資料,可能有延遲,僅供資訊參考,不構成投資建議。"

// errInternal 是資料庫發生未預期錯誤時,回給用戶端的通用訊息。真正的
// 錯誤細節(例如連線逾時的具體原因)只透過 logf 寫入伺服器端 log,絕不
// 可原樣外洩給呼叫端——外洩內部錯誤細節可能讓惡意使用者藉此推測資料庫
// 架構或觸發進一步攻擊。
const errInternal = "伺服器內部發生未預期錯誤,請稍後再試。"

// Querier 是 tool 層對資料查詢的最小需求介面,只列出這四個工具真正會
// 用到的四個方法。
type Querier interface {
	SearchStock(ctx context.Context, query string, limit int) ([]Stock, error)
	LatestDailyQuote(ctx context.Context, symbol string) (*LatestDailyQuote, error)
	PriceHistory(ctx context.Context, symbol string, from, to *time.Time, limit int, adjusted bool) ([]HistoricalQuote, error)
	StockProfile(ctx context.Context, symbol string) (*Profile, error)
}

// AddTools 把全部工具註冊到 MCP server,是本套件對外暴露 MCP 能力的
// 入口函式,通常在 main.go 裡呼叫一次。
//
// # 參數
//   - server:go-sdk 提供的 *mcp.Server 實例,工具會被掛載到這個伺服器上。
//   - q:資料查詢介面,正式環境是 *APIClient;測試注入假資料來源。
//   - logf:記錄資料庫錯誤用的函式,可為 nil(這裡會用一個「什麼都不做」
//     的空函式頂替,避免呼叫端忘記傳而導致到處要判斷 nil)。之所以用
//     一個簡單的 func(string, ...any) 簽名,而不是直接依賴某個特定的
//     log 套件型別,是為了讓本套件跟「用什麼方式記 log」這件事解耦——
//     呼叫端可以把這個函式接到 slog、標準庫 log,或任何自訂的 logger。
func AddTools(server *mcp.Server, q Querier, logf func(format string, args ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ts := &toolset{q: q, logf: logf}
	addDeepDiveTool(server, q, logf)
	addEtfTrackingTool(server, q, logf)

	// mcp.AddTool 是 go-sdk 提供的泛型函式(Go 1.18 起支援的語言特性):
	// 它的完整簽名類似 AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])。
	// 泛型讓同一個 AddTool 函式可以搭配不同的輸入/輸出型別使用(這裡分別
	// 是 SearchStockInput/any、SymbolInput/any 等),SDK 會在內部用 Go 的
	// 反射(reflection)機制,從 In 這個型別自動推導出 JSON Schema 的
	// 基本骨架;但本專案選擇額外手寫更精確的 InputSchema(見下方
	// searchStockSchema 等函式),因為手寫可以精確控制 MinLength、
	// Pattern、Default 這些細節,比單純用反射推導更貼近規格書要求。
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_stock",
		Description: "以股票代號或中文/英文名稱關鍵字搜尋台股股票基本資料。",
		InputSchema: searchStockSchema(),
		// ReadOnlyHint 是給 MCP 用戶端(通常是 LLM 執行環境)的提示,
		// 表明這個工具只會讀取資料、不會產生任何副作用(例如寫入資料庫、
		// 呼叫外部 API 造成扣款等)。部分 MCP 用戶端會依這個提示決定
		// 是否需要額外跟使用者確認才能呼叫這個工具;本專案四個工具全部
		// 是唯讀查詢,因此都標記為 true。
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, ts.searchStock)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_latest_daily_quote",
		Description: "查詢指定股票代號在資料庫中最新可取得的一筆日報價。",
		InputSchema: symbolOnlySchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, ts.latestDailyQuote)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_price_history",
		Description: "查詢歷史日線；adjusted=true 附除權息、減資、分割還原價格，適合長期比較。",
		InputSchema: priceHistorySchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, ts.priceHistory)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_stock_profile",
		Description: "查詢指定股票代號的完整基本面資訊:基本資料、最新報價、近一季/近四季 EPS、每股淨值、ROE、權值、發行股數與歷史高低點。",
		InputSchema: symbolOnlySchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, ts.stockProfile)
	if snapshots, ok := q.(SnapshotQuerier); ok {
		mcp.AddTool(server, &mcp.Tool{Name: "get_realtime_snapshot", Description: "查詢第三方採集的近即時股票報價快照，可能有數秒至數分鐘延遲。", InputSchema: symbolOnlySchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, (&snapshotToolset{snapshots: snapshots, logf: logf}).realtimeSnapshot)
	}

	// FinancialQuerier 採用與 SnapshotQuerier 相同的型別斷言註冊:只有
	// 注入的資料來源真的具備 Phase 1 三種歷史查詢能力(目前僅
	// *APIClient)時才註冊對應工具。只實作基本 Querier 的資料來源沒有這個
	// 介面,因此不會對使用者暴露「呼叫了一定失敗」的工具。
	if financials, ok := q.(FinancialQuerier); ok {
		fts := &financialToolset{financials: financials, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_monthly_revenue_history",
			Description: "查詢指定股票的月營收歷史(當月/累計營收、月增率、年增率),可選填月份區間與筆數上限。資料為歷史彙整,非即時。",
			InputSchema: monthlyRevenueSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.monthlyRevenueHistory)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_financial_statement_history",
			Description: "查詢指定股票的季/年度財報歷史(毛利率、營益率、EPS、ROE、ROA、每股淨值等),period_type 可選 quarterly、annual 或 all。",
			InputSchema: statementHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.financialStatementHistory)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_dividend_history",
			Description: "查詢指定股票的歷年股利發放(現金/股票股利、盈餘分配率、除權息日與發放日),年份篩選依股利所屬年度。",
			InputSchema: dividendHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.dividendHistory)
	}

	// AnalyticsQuerier 與 FinancialQuerier 分開做能力偵測，讓部署期間可先
	// 上線 Phase 1 而不提前暴露 Phase 2。只有注入的 client 三個方法都
	// 實作完成時，tools/list 才會出現這組估值與市場分析工具。
	if analytics, ok := q.(AnalyticsQuerier); ok {
		ats := &analyticsToolset{analytics: analytics, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_stock_valuation",
			Description: "查詢指定股票最新或指定日期以前最近一筆估值模型結果與估值區間；這些分界不是目標價或買賣建議。",
			InputSchema: valuationSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ats.stockValuation)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_market_breadth",
			Description: "查詢統計表既有市場列的漲跌家數、52 週新高／新低家數、均線位置與估值分布；all 為上市＋上櫃合併統計列（市場 id 0，不含興櫃與公開發行），可回傳最多 60 個資料日。",
			InputSchema: marketBreadthSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ats.marketBreadth)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_dividend_yield_ranking",
			Description: "查詢上市及/或上櫃股票的歷史殖利率排行，可依日期與產業篩選；結果僅描述歷史資料，不構成投資建議。",
			InputSchema: yieldRankingSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ats.dividendYieldRanking)
	}

	// StockScreener 是獨立能力，避免為了單一 Phase 3 endpoint 擴大既有
	// Querier。部署期間若 Data API client 尚未具備此方法，工具不會出現，
	// 比註冊一個必然失敗的入口更能準確反映 server 能力。
	if screener, ok := q.(Screener); ok {
		sts := &screenToolset{screener: screener, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "screen_stocks",
			Description: "依估值、營收動能（連續年增、最新月營收下限）、EPS、ROE、殖利率、F 分數、毛利率、負債比、自由現金流、RS 與法人連買篩選上市櫃股票；可排序，非投資建議。",
			InputSchema: screenStocksSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, sts.screenStocks)
	}

	// MarketDataQuerier 是 Phase 4 的獨立能力偵測:三個市場輔助工具彼此
	// 獨立、與前三個 Phase 也無依賴,只有注入的資料來源真的實作這組介面
	// (目前僅 *APIClient)時才註冊,原則與上面各能力群組一致。
	if marketData, ok := q.(MarketDataQuerier); ok {
		mts := &marketDataToolset{marketData: marketData, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_market_index_history",
			Description: "查詢台股大盤 TAIEX 加權指數的歷史走勢(收盤指數、漲跌點數、成交金額/筆數/股數),依日期新到舊排序。與 get_market_breadth 互補:前者看指數點位,後者看市場內部強弱。",
			InputSchema: indexHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, mts.marketIndexHistory)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_dividend_calendar",
			Description: "查詢日期區間內的除權息與股利發放行事曆(除息/除權/現金股利發放/股票股利發放四種事件),依事件日期由近到遠排序;區間最長 92 天,未提供時預設查詢當日起 30 天。",
			InputSchema: dividendCalendarSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, mts.dividendCalendar)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_qfii_holding_ranking",
			Description: "查詢外資(QFII)持股比例或持股數排行,可依市場與產業篩選。注意:這是最近一次每日更新的當前快照,沒有歷史序列,無法回答外資增減持趨勢問題。",
			InputSchema: qfiiRankingSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, mts.qfiiHoldingRanking)
		mcp.AddTool(server, &mcp.Tool{
			Name: "get_market_movers",
			Description: "查詢當日漲跌幅或成交量排行(漲幅前 N 名、跌幅前 N 名、成交量前 N 名),可依市場篩選。" +
				"盤中回傳即時排行,收盤後回傳當日最終排行,資料來源由伺服器端自動判斷並以 source/is_realtime/data_as_of 回報。" +
				"注意:13:30 收盤到 15:00 之間當日收盤資料尚未產生,此時回傳的是前一交易日的排行,摘要會明確標示。",
			InputSchema: moversSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, mts.marketMovers)
	}

	// 籌碼工具同樣以型別斷言偵測能力:只有 *APIClient 實作 ChipQuerier。
	if chips, ok := q.(ChipQuerier); ok {
		cts := &chipToolset{chips: chips, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_chip_data",
			Description: "查詢個股三大法人買賣超（股）、融資融券（張）、連買賣天數、大戶、董監與券商分點籌碼。",
			InputSchema: chipSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, cts.stockChip)
	}

	// 三大報表工具同樣以型別斷言偵測能力:只有 *APIClient 實作 FinancialReportQuerier。
	if reports, ok := q.(FinancialReportQuerier); ok {
		rts := &financialReportToolset{reports: reports, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_financial_reports",
			Description: "查詢個股損益表、資產負債表與現金流量表；可選季報或年報。",
			InputSchema: financialReportSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, rts.financialReports)
	}

	if health, ok := q.(FinancialHealthQuerier); ok {
		hts := &financialHealthToolset{health: health, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_financial_health",
			Description: "查詢 Piotroski F 分數與九項年度財務體質指標、比較年度及缺漏資料。",
			InputSchema: financialHealthSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.financialHealth)
	}

	if fill, ok := q.(DividendFillQuerier); ok {
		dts := &dividendFillToolset{fill: fill, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_dividend_fill",
			Description: "查詢個股歷次除權息填息進度、是否填息、交易天數及歷史統計。",
			InputSchema: dividendFillSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, dts.dividendFill)
	}

	if band, ok := q.(ValuationBandQuerier); ok {
		vts := &valuationBandToolset{band: band, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_valuation_band",
			Description: "查詢 PE／PB 歷史百分位與分位數對應股價；不是目標價。",
			InputSchema: valuationBandSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, vts.valuationBand)
	}

	if peers, ok := q.(PeersQuerier); ok {
		pts := &peersToolset{peers: peers, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_stock_peers",
			Description: "比較同產業個股市值、估值、營收、獲利及財務體質；不含 ETF。",
			InputSchema: peersSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, pts.stockPeers)
	}

	if technicals, ok := q.(TechnicalsQuerier); ok {
		tts := &technicalsToolset{technicals: technicals, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_technical_indicators",
			Description: "查詢個股還原日線技術指標：均線、乖離、KD、MACD、RSI、布林通道與訊號。",
			InputSchema: technicalsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, tts.technicalIndicators)
	}

	if signals, ok := q.(SignalsQuerier); ok {
		sts := &signalsToolset{signals: signals, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_signal_stocks",
			Description: "列出指定交易日出現均線、KD、MACD、RS 或價量訊號的上市櫃股票。",
			InputSchema: signalsSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, sts.signalStocks)
	}

	if history, ok := q.(SignalHistoryQuerier); ok {
		hts := &signalHistoryToolset{history: history, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_signal_performance",
			Description: "統計技術訊號之後各持有期間的含息報酬、勝率及大盤比較；歷史表現非預測。",
			InputSchema: signalPerformanceSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.signalPerformance)
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_stock_signal_history",
			Description: "列出個股歷史技術訊號、當日價格及訊號後各期間含息報酬。",
			InputSchema: stockSignalHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.stockSignalHistory)
	}

	if tq, ok := q.(StockTimelineQuerier); ok {
		tts := &stockTimelineToolset{tq: tq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_stock_timeline",
			Description: "查詢個股除權息、股利、減資分割、營收、財報與技術訊號時間軸。",
			InputSchema: stockTimelineSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, tts.stockTimeline)
	}

	if rs, ok := q.(RelativeStrengthQuerier); ok {
		rts := &relativeStrengthToolset{rs: rs, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_relative_strength_ranking",
			Description: "查詢上市櫃普通股 RS 排行（60／120／240 日還原報酬加權百分位）；99 最強。",
			InputSchema: relativeStrengthSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, rts.relativeStrength)
	}

	if rs, ok := q.(RelativeStrengthHistoryQuerier); ok {
		hts := &relativeStrengthHistoryToolset{rs: rs, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_relative_strength_history",
			Description: "查詢個股 RS 分數、排名及同日母體規模的歷史走勢；不含 ETF、特別股。",
			InputSchema: relativeStrengthHistorySchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, hts.relativeStrengthHistory)
	}

	if is, ok := q.(IndustryStrengthQuerier); ok {
		ists := &industryStrengthToolset{is: is, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_industry_strength",
			Description: "查詢產業 RS 中位數、強勢股比例及領先個股，辨識產業輪動。",
			InputSchema: industryStrengthSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, ists.industryStrength)
	}

	if fq, ok := q.(InstitutionalFlowQuerier); ok {
		fts := &institutionalFlowToolset{fq: fq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_institutional_flow_ranking",
			Description: "查詢法人買賣超排行、連續買賣天數與成交占比；區分股數及金額。",
			InputSchema: institutionalFlowSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, fts.institutionalFlow)
	}

	if sq, ok := q.(InstitutionalStreakQuerier); ok {
		sts := &institutionalStreakToolset{sq: sq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_institutional_streak_performance",
			Description: "統計法人連續買超或賣超滿 N 天之後的含息報酬、勝率與大盤比較。",
			InputSchema: institutionalStreakSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, sts.institutionalStreakPerformance)
	}

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

	if tq, ok := q.(StrategyTrackQuerier); ok {
		sts := &strategyTrackToolset{tq: tq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_strategy_track",
			Description: "查詢具名策略實盤記錄的逐期持股、權重與績效；與事後回測分開。",
			InputSchema: strategyTrackSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, sts.strategyTrack)
	}

	if fq, ok := q.(IndustryFlowQuerier); ok {
		its := &industryFlowToolset{fq: fq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_industry_flow",
			Description: "查詢三大法人各產業買賣超金額、股數與集中方向。",
			InputSchema: industryFlowSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, its.industryFlow)
	}

	if mq, ok := q.(MarginRankingQuerier); ok {
		mts := &marginRankingToolset{mq: mq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_margin_ranking",
			Description: "查詢融資融券增減或券資比排行；可依市場及天數篩選。",
			InputSchema: marginRankingSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, mts.marginRanking)
	}

	if rq, ok := q.(MarketRegimeQuerier); ok {
		rts := &marketRegimeToolset{rq: rq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_market_regime",
			Description: "查詢 TAIEX 趨勢狀態、均線位置方向、波動與回撤；不代表未來行情。",
			InputSchema: marketRegimeSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, rts.marketRegime)
	}

	if vq, ok := q.(MarketValuationQuerier); ok {
		vts := &marketValuationToolset{vq: vq, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_market_valuation",
			Description: "查詢上市櫃官方 PE／PB 每日中位數及歷史百分位；不含虧損股與 ETF。",
			InputSchema: marketValuationSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, vts.marketValuation)
	}

	if industries, ok := q.(IndustriesQuerier); ok {
		its := &industriesToolset{industries: industries, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_industry_performance",
			Description: "查詢產業市值加權報酬、中位數、上漲比例及領漲跌個股；不含 ETF。",
			InputSchema: industriesSchema(),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, its.industryPerformance)
	}

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
