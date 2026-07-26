package stock

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
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
//
// ## 給新手的背景知識:MCP 是什麼、為什麼工具要回傳「文字 + 結構化資料」?
//
// MCP(Model Context Protocol)是一種讓 LLM(大型語言模型)呼叫外部工具、
// 取得外部資料的通訊協定。當 LLM 想查一支股票的報價,它會呼叫這裡定義的
// 某個工具(例如 get_latest_daily_quote),並且期待收到兩種資訊:
//   - 一段人類可讀的文字摘要(content),LLM 可以直接讀懂並組織成自然
//     語言回答給使用者。
//   - 一份結構化的 JSON 資料(structuredContent),讓需要精確數字的下游
//     處理(例如程式化的後續運算)可以直接讀取欄位,不需要去解析文字。
//
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
//
// ## 給 Go 新手的背景知識:為什麼介面定義在這裡(使用端),而不是定義在
// repository.go(實作端)?
//
// 這是 Go 語言慣用的「介面應該由消費者定義」原則,跟其他語言(例如
// Java、C#)常見的「先定義介面、再寫實作」順序恰好相反。Go 的介面是
// 「隱式實作」(implicit implementation):任何型別只要擁有介面要求的
// 全部方法(方法名稱、參數、回傳值都吻合),就自動被視為「實作了這個
// 介面」,完全不需要在型別上寫任何類似 `implements Querier` 的宣告。
//
// 把 Querier 定義在 tools.go(呼叫端)而不是 repository.go(*Repository
// 這個實際實作所在的檔案),帶來兩個好處:
//   - *Repository 完全不需要知道 Querier 這個介面存在,未來如果要換成
//     直接呼叫 stock_rust 的 WebAPI(而不是連資料庫),只要新寫一個同樣
//     擁有這四個方法的型別(例如 *APIClient),不需要改動 *Repository
//     或這個介面本身的任何一行程式碼。
//   - 測試的時候可以輕鬆寫一個「假的」(fake)實作(見
//     tools_test.go 的 fakeQuerier),不需要真的連資料庫,也不需要引入
//     mock 產生工具——這正是 Go 官方測試慣例偏好的「手寫 fake」風格。
type Querier interface {
	SearchStock(ctx context.Context, query string, limit int) ([]Stock, error)
	LatestDailyQuote(ctx context.Context, symbol string) (*LatestDailyQuote, error)
	PriceHistory(ctx context.Context, symbol string, from, to *time.Time, limit int) ([]HistoricalQuote, error)
	StockProfile(ctx context.Context, symbol string) (*StockProfile, error)
}

// AddTools 把四個 tool 註冊到 MCP server,是本套件對外暴露 MCP 能力的
// 入口函式,通常在 main.go 裡呼叫一次。
//
// # 參數
//   - server:go-sdk 提供的 *mcp.Server 實例,工具會被掛載到這個伺服器上。
//   - q:資料查詢介面,通常是 *Repository(直連資料庫)的實例,由呼叫端
//     決定要注入哪一種實作。
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
		Description: "查詢指定股票代號的歷史日線資料,可選填日期範圍與筆數上限。",
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
	// *APIClient)時才註冊對應工具。db 模式的 *Repository 沒有實作這個
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
			Description: "查詢統計表既有市場列的漲跌家數、均線位置與估值分布；all 使用市場 id 0 的全市場合併統計列，可回傳最多 60 個資料日。",
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
	if screener, ok := q.(StockScreener); ok {
		sts := &screenToolset{screener: screener, logf: logf}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "screen_stocks",
			Description: "依市場、產業、估值區間、營收年增率、EPS、ROE 或殖利率等固定白名單條件篩選股票；只描述符合條件的歷史資料，不替使用者做投資決策。",
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
	}
}

// snapshotToolset 將獨立快照介面與記錄函式綁定，避免擴充既有日線 Querier。
type snapshotToolset struct {
	snapshots SnapshotQuerier
	logf      func(string, ...any)
}

// realtimeSnapshot 執行 get_realtime_snapshot 並明確標示資料不是保證即時行情。
func (ts *snapshotToolset) realtimeSnapshot(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := ts.snapshots.RealtimeSnapshot(ctx, symbol)
	if err != nil {
		ts.logf("工具 get_realtime_snapshot 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if snapshot == nil {
		return nil, nil, fmt.Errorf("查無此股票的即時報價快照；可改用 get_latest_daily_quote 查詢最近收盤資料")
	}
	out := struct {
		DataKind   string            `json:"data_kind"`
		DataAsOf   string            `json:"data_as_of"`
		IsRealtime bool              `json:"is_realtime"`
		Disclaimer string            `json:"disclaimer"`
		Snapshot   *RealtimeSnapshot `json:"snapshot"`
	}{"realtime_snapshot", snapshot.UpdatedAt, false, "本資料為盤中由第三方站點採集的近即時報價快照,可能有數秒至數分鐘延遲,非交易所保證即時行情,僅供資訊參考。", snapshot}
	return textResult(fmt.Sprintf("股票名稱:%s (%s)\n近即時價格:%s\n免責聲明:%s", snapshot.Name, snapshot.StockSymbol, displayFloat(snapshot.Price), out.Disclaimer)), out, nil
}

// toolset 把「查詢介面」與「錯誤記錄函式」打包在一起,四個工具方法
// (searchStock、latestDailyQuote 等)都掛在這個型別上,這樣它們可以
// 共用同一份 q 與 logf,不需要每個工具方法都各自接收這兩個參數。
type toolset struct {
	q    Querier
	logf func(format string, args ...any)
}

// ---------------------------------------------------------------------------
// 輸入型別與 JSON Schema
// ---------------------------------------------------------------------------
//
// 這個區塊定義四個工具各自的輸入參數型別,以及對應的 JSON Schema。
//
// ## 給新手的背景知識:什麼是 JSON Schema,為什麼 MCP 工具需要它?
//
// JSON Schema 是一種用 JSON 格式描述「另一份 JSON 資料應該長什麼樣子」
// 的規格語言,例如「這個欄位是字串、最少 1 個字、最多 100 個字」。MCP
// 協定要求每個工具在 tools/list 回應裡附上輸入參數的 JSON Schema,讓
// 呼叫工具的 LLM 執行環境能夠:
//   - 知道呼叫這個工具需要提供哪些參數、參數的型別與限制。
//   - 在真正發送呼叫請求之前,先在用戶端做基本的格式檢查。
//
// 這裡的 JSON Schema 是手寫的(而不是靠 go-sdk 從 Go 型別自動反射推導),
// 因為手寫可以精確表達 MinLength、MaxLength、Pattern(正則表達式)、
// Default(預設值)這些規格書明確要求的驗證規則。

// SearchStockInput 是 search_stock 工具的輸入參數型別。go-sdk 會把
// MCP 呼叫方傳來的 JSON 引數,依照這個型別上的 `json:"..."` 標籤自動
// 解析(unmarshal)成這個 struct 的實例,交給對應的工具方法處理。
type SearchStockInput struct {
	Query string `json:"query"`
	// Limit 用 `json:"limit,omitempty"`:當 JSON 裡沒有提供 limit 欄位
	// (或值剛好是 Go int 的零值 0)時,Limit 會是 0——本檔案後面的
	// rangedLimit 函式會把「收到 0」解讀為「呼叫端沒有提供這個參數」,
	// 套用預設值,而不是真的把 limit 當成 0 筆處理。
	Limit int `json:"limit,omitempty"`
}

// SymbolInput 是只需要股票代號的工具(get_latest_daily_quote、
// get_stock_profile)共用的輸入參數型別。
type SymbolInput struct {
	Symbol string `json:"symbol"`
}

// PriceHistoryInput 是 get_price_history 工具的輸入參數型別。
type PriceHistoryInput struct {
	Symbol string `json:"symbol"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// searchStockSchema 描述 search_stock 工具的輸入參數形狀:query 是
// 1 到 100 字元的必要字串,limit 是選填整數(預設 10、範圍 1 到 50)。
func searchStockSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"query": {
				Type:        "string",
				MinLength:   ptr(1),
				MaxLength:   ptr(100),
				Description: "搜尋關鍵字(股票代號、中文或英文名稱)",
			},
			"limit": {
				Type:    "integer",
				Minimum: ptr(1.0),
				Maximum: ptr(50.0),
				// Default 欄位在 jsonschema.Schema 裡的型別是
				// json.RawMessage(也就是 []byte),必須是「已經編碼成
				// JSON 語法」的原始位元組,不能直接放 Go 的 int 字面值,
				// 所以這裡寫的是代表 JSON 數字 10 的位元組序列 []byte("10")。
				Default:     []byte("10"),
				Description: "最大回傳筆數(預設 10,範圍 1 至 50)",
			},
		},
		Required: []string{"query"},
	}
}

// symbolOnlySchema 是 get_latest_daily_quote / get_stock_profile 共用的
// schema:只有一個必要的 symbol 字串欄位。
func symbolOnlySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
		},
		Required: []string{"symbol"},
	}
}

// priceHistorySchema 描述 get_price_history 工具的輸入參數形狀。
// from/to 用 Pattern(正則表達式)限制必須符合 YYYY-MM-DD 格式;
// 這只是「格式」層面的檢查,「日期是否真的存在」(例如 2026-13-40 這種
// 格式對但日期不合法的輸入)由 parseDateArg 在程式碼裡進一步驗證。
func priceHistorySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "起始日期(格式 YYYY-MM-DD,選填)",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "結束日期(格式 YYYY-MM-DD,選填)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(365.0),
				Default:     []byte("30"),
				Description: "最大回傳筆數(預設 30,範圍 1 至 365)",
			},
		},
		Required: []string{"symbol"},
	}
}

// symbolSchema 是「股票代號」欄位共用的 schema 定義(1 到 24 字元的
// 必要字串),被 symbolOnlySchema 與 priceHistorySchema 共用,避免同一份
// 規則在多處重複維護。
func symbolSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		MinLength:   ptr(1),
		MaxLength:   ptr(24),
		Description: "股票代號(如 2330)",
	}
}

// ptr 是一個小工具泛型函式:把任何值 v 包成指向它的指標並回傳。
//
// ## 給新手的背景知識:為什麼需要這個函式?
//
// 在 Go 裡,你不能對一個「字面值」直接取址(例如 &1 是不合法的語法),
// 只能對一個已經存在的變數取址(例如 x := 1; &x 才合法)。但
// jsonschema.Schema 裡的 MinLength、Maximum 等欄位型別是 *int、*float64
// (用指標表達「這個限制是否有被設定」,沒設定就是 nil,道理與 models.go
// 用指標表達 NULL 是同一個原則),如果要內嵌著寫 MinLength: ptr(1) 這種
// 寫法,而不是每次都先宣告一個變數再取址,就需要這個小工具函式幫忙——
// 呼叫 ptr(1) 內部會建立一個區域變數 v 存放 1,再回傳 &v。
//
// [T any] 是 Go 泛型的語法:T 是型別參數,any 是它的限制條件(代表
// 「任何型別都可以」),讓 ptr 可以用在 ptr(1)(T 是 int)、
// ptr(1.0)(T 是 float64)等各種型別上,不需要為每種型別各寫一份。
func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// 輸出型別(structuredContent 的形狀)
// ---------------------------------------------------------------------------
//
// 以下四個型別對應 mcp.CallToolResult 裡 StructuredContent 的實際內容。
// 依規格書要求,任何「價格資料」都必須包含 DataKind(這筆資料屬於哪一種
// 查詢)、DataAsOf(資料的時間基準)、IsRealtime(是否為即時行情——本
// 服務目前一律是 false)、Disclaimer(免責聲明文字)這四個共通欄位。

// SearchStockOutput 是 search_stock 的 structuredContent。
type SearchStockOutput struct {
	Stocks []Stock `json:"stocks"`
}

// LatestQuoteOutput 是 get_latest_daily_quote 的 structuredContent。
type LatestQuoteOutput struct {
	DataKind   string      `json:"data_kind"`
	DataAsOf   *string     `json:"data_as_of"`
	IsRealtime bool        `json:"is_realtime"`
	Disclaimer string      `json:"disclaimer"`
	Stock      Stock       `json:"stock"`
	Quote      *DailyQuote `json:"quote"`
}

// PriceHistoryOutput 是 get_price_history 的 structuredContent。
type PriceHistoryOutput struct {
	DataKind   string            `json:"data_kind"`
	DataAsOf   *string           `json:"data_as_of"`
	IsRealtime bool              `json:"is_realtime"`
	Disclaimer string            `json:"disclaimer"`
	Quotes     []HistoricalQuote `json:"quotes"`
}

// StockProfileOutput 是 get_stock_profile 的 structuredContent。
type StockProfileOutput struct {
	DataKind   string       `json:"data_kind"`
	DataAsOf   *string      `json:"data_as_of"`
	IsRealtime bool         `json:"is_realtime"`
	Disclaimer string       `json:"disclaimer"`
	Profile    StockProfile `json:"profile"`
}

// ---------------------------------------------------------------------------
// 輸入驗證輔助函式
// ---------------------------------------------------------------------------
//
// 這個區塊的函式在「呼叫資料庫之前」先檢查輸入是否合法,驗證失敗時回傳
// 的 error 訊息會被下面的工具方法直接當作 MCP 工具錯誤（isError: true）
// 回給呼叫端,因此每一則錯誤訊息都刻意寫成「使用者/LLM 看得懂、能據此
// 修正輸入」的完整句子,而不是像 Go 內部常見的簡短小寫錯誤片段。

// datePattern 用來檢查日期字串的「格式」是否為四位數字-二位數字-二位
// 數字,例如 "2026-07-13"。這只驗證格式,不驗證日期是否真實存在(例如
// "2026-13-40" 會通過這個正則表達式,但不是一個真實存在的日期)——
// 真實性檢查交給 time.Parse 處理,見 parseDateArg。
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validateLength 以 Unicode 字元數(而非 byte 數)驗證字串長度。
//
// ## 給新手的背景知識:為什麼不能直接用 len(value)?
//
// Go 的 len() 函式作用在字串上時,回傳的是「UTF-8 編碼後的位元組數」,
// 而不是「字元數」。中文字元(例如「台」)在 UTF-8 編碼下通常占 3 個
// byte,如果限制「最多 100 字元」卻用 len() 檢查,使用者輸入 34 個中文
// 字就會被誤判成超過 100(34 × 3 = 102),跟使用者「我明明只打了 34
// 個字」的直覺完全不符。
//
// utf8.RuneCountInString 會正確地把字串依 Unicode 字元(Go 稱為 rune)
// 切分後計數,不管是英文字母(1 byte)還是中文字(3 byte),都算作
// 1 個字元,這才符合一般人對「字數」的認知。
func validateLength(value, field string, minLen, maxLen int) error {
	n := utf8.RuneCountInString(value)
	if n < minLen || n > maxLen {
		return fmt.Errorf("參數 %s 長度必須介於 %d 到 %d 字元之間,目前為 %d 字元", field, minLen, maxLen, n)
	}
	return nil
}

// normalizeSymbol 驗證並正規化股票代號:先確認長度(trim 前後空白後
// 1 到 24 字元),再統一轉成大寫(例如 "00631l" 轉成 "00631L")。
//
// 把「正規化規則」集中寫在這一個函式,是為了確保「拿去查資料庫的值」跟
// 「查不到時回錯誤訊息用的值」永遠是同一套規則處理出來的結果,不會有
// 兩處各自轉換、卻不小心寫得不一致的風險。
func normalizeSymbol(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if err := validateLength(s, "symbol", 1, 24); err != nil {
		return "", err
	}
	return strings.ToUpper(s), nil
}

// rangedLimit 套用 limit 參數的預設值與範圍限制。
//
// value 等於 0 時視為「呼叫端沒有提供這個參數」——這個判斷方式依賴一個
// Go 特性:int 型別在 JSON 解析時,如果 JSON 裡沒有這個欄位,對應的
// struct 欄位會維持 Go 的零值(int 的零值就是 0),所以「沒提供」跟
// 「使用者明確傳了 0」在這裡是無法分辨的兩種情況——但因為業務邏輯上
// 「查詢筆數上限是 0」本來就沒有意義(規格書要求範圍是 1 起跳),把
// 兩者都視為「使用預設值」不會造成誤判。
//
// 超出範圍時明確回傳錯誤,而不是靜默把值夾到邊界(例如把 999 硬夾成
// 50)——如果靜默夾邊界,使用者可能誤以為自己輸入的數字真的被採用了,
// 明確報錯比較誠實,也能讓使用者立刻發現輸入有誤。
func rangedLimit(value, def, min, max int) (int, error) {
	if value == 0 {
		return def, nil
	}
	if value < min || value > max {
		return 0, fmt.Errorf("參數 limit 必須介於 %d 到 %d 之間,收到了 %d", min, max, value)
	}
	return value, nil
}

// parseDateArg 解析並驗證 YYYY-MM-DD 格式的日期字串;空字串代表呼叫端
// 沒有提供這個參數,回傳 (nil, nil)。
//
// 這裡分兩層驗證:先用 datePattern 這個正則表達式檢查「格式」對不對
// (四位數-二位數-二位數),格式對了才進一步用 time.Parse 檢查「日期
// 是否真實存在」——例如 "2026-13-40" 符合正則表達式的格式,但 13 月、
// 40 日並不是真實存在的日期,time.Parse 在這種情況下會回傳錯誤,被
// 這個函式攔下來轉成使用者看得懂的訊息。
func parseDateArg(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	if !datePattern.MatchString(raw) {
		return nil, fmt.Errorf("參數 %s 日期格式必須為 YYYY-MM-DD", field)
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("參數 %s 日期格式必須為 YYYY-MM-DD", field)
	}
	return &t, nil
}

// ---------------------------------------------------------------------------
// 工具實作
// ---------------------------------------------------------------------------
//
// 以下四個方法是四個 MCP 工具真正的執行邏輯,簽名都符合 go-sdk 要求的
// ToolHandlerFor[In, Out] 泛型形狀:
//
//	func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error)
//
// 回傳值有三個:
//   - *mcp.CallToolResult:給人類/LLM 讀的文字摘要(這裡都是呼叫
//     textResult 組出來的),structuredContent 由 SDK 依第二個回傳值
//     自動填入,不需要手動組裝。
//   - Out(這裡型別是 any):structuredContent 實際要輸出的資料;SDK 看到
//     這裡不是 nil,會自動序列化並放進 CallToolResult.StructuredContent。
//   - error:非 nil 時,SDK 會自動把整個結果轉成「isError: true」的
//     MCP 工具錯誤(見本檔案開頭的說明),因此這裡回傳的每一個 error
//     訊息都必須是可以安全顯示給呼叫端的文字。

// searchStock 執行 search_stock 工具:驗證輸入 → 查詢 → 組出摘要與
// 結構化輸出。
func (ts *toolset) searchStock(ctx context.Context, _ *mcp.CallToolRequest, in SearchStockInput) (*mcp.CallToolResult, any, error) {
	if err := validateLength(in.Query, "query", 1, 100); err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 10, 1, 50)
	if err != nil {
		return nil, nil, err
	}

	stocks, err := ts.q.SearchStock(ctx, in.Query, limit)
	if err != nil {
		// 資料庫錯誤的完整細節寫進伺服器端 log(ts.logf),但回給呼叫端
		// 的訊息維持通用、不含任何內部細節——這是本檔案開頭說明的安全
		// 規則在這裡的具體實踐。
		ts.logf("工具 search_stock 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if stocks == nil {
		// Repository.SearchStock 理論上已經保證回傳空 slice 而非 nil,
		// 這裡再檢查一次是防禦性寫法:即使未來 Querier 換成別的實作
		// (例如改連 WebAPI)不小心讓某個路徑回傳了 nil slice,這裡也能
		// 攔下來,確保「查無資料」在 JSON 輸出永遠是 []而不是 null。
		stocks = []Stock{}
	}

	summary := "找不到符合關鍵字的股票。"
	if len(stocks) > 0 {
		summary = fmt.Sprintf("搜尋到 %d 檔股票。", len(stocks))
	}
	return textResult(summary), SearchStockOutput{Stocks: stocks}, nil
}

// latestDailyQuote 執行 get_latest_daily_quote 工具。
func (ts *toolset) latestDailyQuote(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}

	latest, err := ts.q.LatestDailyQuote(ctx, symbol)
	if err != nil {
		ts.logf("工具 get_latest_daily_quote 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if latest == nil {
		// latest 是 nil 代表 Repository 找不到這個股票代號(見
		// repository.go 的 LatestDailyQuote 說明);這裡用 in.Symbol
		// (使用者原始輸入,而非正規化後的 symbol)組錯誤訊息,讓使用者
		// 看到的訊息跟自己輸入的內容一致,不會因為被轉成大寫而感到困惑。
		return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
	}

	// strings.Builder 是 Go 標準函式庫提供的「可變字串緩衝區」,適合
	// 像這裡「要用好幾個 Fprintf 陸續拼接出一段長文字」的情境——直接用
	// 字串的 += 運算子重複串接,每一次串接都會複製整個字串內容,在
	// 迴圈或多次拼接的情境下效能較差;strings.Builder 內部用可成長的
	// byte slice 累積內容,只在真正需要輸出時才轉成最終字串。
	var b strings.Builder
	fmt.Fprintf(&b, "股票名稱:%s (%s)\n", latest.Stock.Name, latest.Stock.StockSymbol)
	if q := latest.Quote; q != nil {
		fmt.Fprintf(&b, "日期:%s\n", q.Date)
		fmt.Fprintf(&b, "收盤價:%s\n", displayFloat(q.ClosingPrice))
		fmt.Fprintf(&b, "漲跌:%s (%s%%)\n", displayFloat(q.Change), displayFloat(q.ChangeRange))
		fmt.Fprintf(&b, "成交量:%s 股\n", displayFloat(q.TradingVolume))
	} else {
		// Quote 是 nil 代表股票存在,但資料庫還沒有這支股票的日報價
		// (見 repository.go 的說明);摘要文字要誠實反映這件事,而不是
		// 假裝有資料卻全部顯示「無」,讓使用者誤以為系統故障。
		b.WriteString("此股票目前在資料庫中沒有最新日報價。\n")
	}
	fmt.Fprintf(&b, "免責聲明:%s", Disclaimer)

	out := LatestQuoteOutput{
		DataKind: "latest_daily_quote",
		// quoteDataAsOf 決定「這筆資料的時間基準」該用哪個時間欄位,
		// 詳細規則見函式本身的說明。
		DataAsOf:   quoteDataAsOf(latest.Quote),
		IsRealtime: false,
		Disclaimer: Disclaimer,
		Stock:      latest.Stock,
		Quote:      latest.Quote,
	}
	return textResult(b.String()), out, nil
}

// priceHistory 執行 get_price_history 工具。
func (ts *toolset) priceHistory(ctx context.Context, _ *mcp.CallToolRequest, in PriceHistoryInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	from, err := parseDateArg(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseDateArg(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	// 起始日期不可晚於結束日期——只有兩者都有提供時才需要比較;任一方
	// 是 nil(未提供)就沒有「誰比較晚」的問題。
	if from != nil && to != nil && from.After(*to) {
		return nil, nil, fmt.Errorf("起始日期 (from) 不可晚於結束日期 (to)")
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 365)
	if err != nil {
		return nil, nil, err
	}

	quotes, err := ts.q.PriceHistory(ctx, symbol, from, to, limit)
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_price_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if quotes == nil {
		quotes = []HistoricalQuote{}
	}

	var summary string
	if len(quotes) == 0 {
		summary = fmt.Sprintf("未找到股票 %s 的歷史日線資料。\n免責聲明:%s", symbol, Disclaimer)
	} else {
		summary = fmt.Sprintf("取得股票 %s 共 %d 筆歷史日線資料。\n免責聲明:%s", symbol, len(quotes), Disclaimer)
	}

	// 查詢結果已依日期新到舊排序(見 repository.go 的
	// "ORDER BY \"Date\" DESC"),因此 quotes 的第一筆就是這批資料裡
	// 最新的一筆,可以直接拿它的日期當作整批資料的 data_as_of,不需要
	// 額外掃描整個 slice 找最大值。
	var dataAsOf *string
	if len(quotes) > 0 {
		dataAsOf = quotes[0].Date
	}

	out := PriceHistoryOutput{
		DataKind:   "price_history",
		DataAsOf:   dataAsOf,
		IsRealtime: false,
		Disclaimer: Disclaimer,
		Quotes:     quotes,
	}
	return textResult(summary), out, nil
}

// stockProfile 執行 get_stock_profile 工具。
func (ts *toolset) stockProfile(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}

	profile, err := ts.q.StockProfile(ctx, symbol)
	if err != nil {
		ts.logf("工具 get_stock_profile 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	if profile == nil {
		return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "股票名稱:%s (%s)\n", profile.Stock.Name, profile.Stock.StockSymbol)
	fmt.Fprintf(&b, "近一季 EPS:%s\n", displayFloat(profile.LastOneEPS))
	fmt.Fprintf(&b, "近四季 EPS:%s\n", displayFloat(profile.LastFourEPS))
	fmt.Fprintf(&b, "每股淨值:%s\n", displayFloat(profile.NetAssetValuePerShare))
	if profile.ReturnOnEquity != nil {
		fmt.Fprintf(&b, "ROE:%s%%\n", formatFloat(*profile.ReturnOnEquity))
	} else {
		b.WriteString("ROE:無\n")
	}
	if profile.Quote != nil {
		fmt.Fprintf(&b, "最新收盤價:%s\n", displayFloat(profile.Quote.ClosingPrice))
	}
	if h := profile.History; h != nil {
		fmt.Fprintf(&b, "歷史最高價:%s (%s)\n", displayFloat(h.MaximumPrice), displayString(h.MaximumPriceDateOn))
		fmt.Fprintf(&b, "歷史最低價:%s (%s)\n", displayFloat(h.MinimumPrice), displayString(h.MinimumPriceDateOn))
	}
	fmt.Fprintf(&b, "免責聲明:%s", Disclaimer)

	out := StockProfileOutput{
		DataKind:   "stock_profile",
		DataAsOf:   quoteDataAsOf(profile.Quote),
		IsRealtime: false,
		Disclaimer: Disclaimer,
		Profile:    *profile,
	}
	return textResult(b.String()), out, nil
}

// ---------------------------------------------------------------------------
// 輸出輔助函式
// ---------------------------------------------------------------------------

// textResult 建立只含一段文字摘要的 mcp.CallToolResult。
//
// structuredContent 欄位刻意不在這裡設定:go-sdk 的 AddTool 機制會在
// handler 回傳後,依第二個回傳值(每個工具方法回傳的 Out,例如
// SearchStockOutput)自動序列化並填入 StructuredContent,本函式只需要
// 負責「文字摘要」這一半。
func textResult(summary string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: summary}},
	}
}

// quoteDataAsOf 依規格決定 data_as_of 欄位該取哪一個時間:優先用
// UpdatedTime(這筆資料最後被更新的時間,最能反映資料新鮮度);
// 如果沒有 UpdatedTime,退而求其次用 RecordTime(資料被寫入的時間);
// 兩者都沒有時,最後用交易日期 Date 頂替(至少讓使用者知道這是哪一天
// 的資料)。沒有報價(q 是 nil)時整個回傳 nil。
func quoteDataAsOf(q *DailyQuote) *string {
	if q == nil {
		return nil
	}
	if q.UpdatedTime != nil {
		return q.UpdatedTime
	}
	if q.RecordTime != nil {
		return q.RecordTime
	}
	d := q.Date
	return &d
}

// displayFloat 把 *float64 格式化成摘要文字給人類閱讀:nil 顯示「無」,
// 避免摘要裡出現像 Go 內部表示法的 "<nil>" 這種對一般使用者毫無意義的
// 字樣。這個函式只用在「文字摘要」(content),structuredContent 裡的
// 數字欄位仍然維持 *float64(nil 就是 nil),兩者互不影響。
func displayFloat(v *float64) string {
	if v == nil {
		return "無"
	}
	return formatFloat(*v)
}

// displayString 對 *string 做跟 displayFloat 相同的「nil 顯示無」處理。
func displayString(v *string) string {
	if v == nil {
		return "無"
	}
	return *v
}

// formatFloat 以「最短可還原表示法」把 float64 轉成字串,例如輸出
// "123.45" 而不是 "123.450000"。
//
// strconv.FormatFloat 的第三個參數(這裡是 -1)是「有效位數」設定,傳
// -1 時 Go 會採用剛好足夠精確重建原始浮點數所需的最少位數,不會像
// fmt.Sprintf("%f", v) 那樣固定輸出六位小數、產生一堆多餘的尾隨零。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ---------------------------------------------------------------------------
// 跨 Phase 共用的市場與日期參數處理
// ---------------------------------------------------------------------------
//
// 以下幾個函式最初是為 Phase 2 寫的,但 Phase 3(選股)與 Phase 4(QFII
// 排行、指數歷史)後來也用到同一套規則,因此集中放在本檔案——共用的東西
// 放在共用的地方,才不會讓「選股」的實作莫名其妙依賴「估值分析」那個
// 檔案裡的函式。

// breadthMarketSchema 描述市場廣度統計表本身的市場列。
//
// `all` 必須忠實描述 Data API 查詢的 id 0 合併列；它和排行／選股直接
// 過濾 stocks 表的 `IN (2, 4)` 不是同一種底層語意，因此不可共用說明。
func breadthMarketSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"all", "twse", "tpex"},
		Default:     []byte(`"all"`),
		Description: "統計列:all(市場 id 0 的全市場合併列)、twse(上市 id 2)或 tpex(上櫃 id 4)",
	}
}

// listedOTCMarketSchema 描述直接過濾股票主檔的上市櫃市場範圍。
// `all` 固定為上市 id 2 加上櫃 id 4，不包含公開發行與興櫃。
func listedOTCMarketSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"all", "twse", "tpex"},
		Default:     []byte(`"all"`),
		Description: "市場:all(上市+上櫃，不含公開發行與興櫃)、twse(上市)或 tpex(上櫃)",
	}
}

// normalizeMarket 套用預設市場並拒絕固定白名單以外的值。
func normalizeMarket(raw string) (string, error) {
	if raw == "" {
		return "all", nil
	}
	market := strings.ToLower(strings.TrimSpace(raw))
	if market != "all" && market != "twse" && market != "tpex" {
		return "", fmt.Errorf("參數 market 必須為 all、twse 或 tpex,收到了 %q", raw)
	}
	return market, nil
}

// parseOptionalDate 驗證日期並保留 Data API 需要的 YYYY-MM-DD 字串。
func parseOptionalDate(raw, field string) (string, error) {
	parsed, err := parseDateArg(raw, field)
	if err != nil || parsed == nil {
		return raw, err
	}
	return parsed.Format("2006-01-02"), nil
}
