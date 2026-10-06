package stock

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 四個核心報價工具(search_stock、get_latest_daily_quote、get_price_history、
// get_stock_profile)與即時快照工具的輸入、schema、輸出與 handler。

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

// ProfileOutput 是 get_stock_profile 的 structuredContent。
type ProfileOutput struct {
	DataKind   string  `json:"data_kind"`
	DataAsOf   *string `json:"data_as_of"`
	IsRealtime bool    `json:"is_realtime"`
	Disclaimer string  `json:"disclaimer"`
	Profile    Profile `json:"profile"`
}

// ---------------------------------------------------------------------------
// 輸入驗證輔助函式
// ---------------------------------------------------------------------------
//
// 這個區塊的函式在「呼叫資料庫之前」先檢查輸入是否合法,驗證失敗時回傳
// 的 error 訊息會被下面的工具方法直接當作 MCP 工具錯誤（isError: true）
// 回給呼叫端,因此每一則錯誤訊息都刻意寫成「使用者/LLM 看得懂、能據此
// 修正輸入」的完整句子,而不是像 Go 內部常見的簡短小寫錯誤片段。

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
		// APIClient.SearchStock 理論上已經保證回傳空 slice 而非 nil,
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
		// latest 是 nil 代表找不到這個股票代號(見
		// apiclient.go 的 LatestDailyQuote 說明);這裡用 in.Symbol
		// (使用者原始輸入,而非正規化後的 symbol)組錯誤訊息,讓使用者
		// 看到的訊息跟自己輸入的內容一致,不會因為被轉成大寫而感到困惑。
		return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
	}

	// strings.Builder 是 Go 標準函式庫提供的「可變字串緩衝區」,適合
	// 像這裡「要用好幾個 Fprintf 陸續拼接出一段長文字」的情境——直接用
	// 字串的 += 運算子重複串接,每一次串接都會複製整個字串內容,在
	// 迴圈或多次拼接的情境下效能較差;strings.Builder 內部用可成長的
	// byte slice 累積內容,只在真正需要輸出時才轉成最終字串。
	var b summaryBuilder
	b.printf("股票名稱:%s (%s)\n", latest.Stock.Name, latest.Stock.StockSymbol)
	if q := latest.Quote; q != nil {
		b.printf("日期:%s\n", q.Date)
		b.printf("收盤價:%s\n", displayFloat(q.ClosingPrice))
		b.printf("漲跌:%s (%s%%)\n", displayFloat(q.Change), displayFloat(q.ChangeRange))
		b.printf("成交量:%s 股\n", displayFloat(q.TradingVolume))
	} else {
		// Quote 是 nil 代表股票存在,但資料庫還沒有這支股票的日報價
		// (見 apiclient.go 的說明);摘要文字要誠實反映這件事,而不是
		// 假裝有資料卻全部顯示「無」,讓使用者誤以為系統故障。
		b.writeString("此股票目前在資料庫中沒有最新日報價。\n")
	}
	b.printf("免責聲明:%s", Disclaimer)

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

	// 查詢結果已依日期新到舊排序(見 apiclient.go 的
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

	var b summaryBuilder
	b.printf("股票名稱:%s (%s)\n", profile.Stock.Name, profile.Stock.StockSymbol)
	b.printf("近一季 EPS:%s\n", displayFloat(profile.LastOneEPS))
	b.printf("近四季 EPS:%s\n", displayFloat(profile.LastFourEPS))
	b.printf("每股淨值:%s\n", displayFloat(profile.NetAssetValuePerShare))
	if profile.ReturnOnEquity != nil {
		b.printf("ROE:%s%%\n", formatFloat(*profile.ReturnOnEquity))
	} else {
		b.writeString("ROE:無\n")
	}
	if profile.Quote != nil {
		b.printf("最新收盤價:%s\n", displayFloat(profile.Quote.ClosingPrice))
	}
	if h := profile.History; h != nil {
		b.printf("歷史最高價:%s (%s)\n", displayFloat(h.MaximumPrice), displayString(h.MaximumPriceDateOn))
		b.printf("歷史最低價:%s (%s)\n", displayFloat(h.MinimumPrice), displayString(h.MinimumPriceDateOn))
	}
	b.printf("免責聲明:%s", Disclaimer)

	out := ProfileOutput{
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
