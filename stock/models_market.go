package stock

import "context"

// 大盤指數、股利行事曆、外資持股排行與漲跌排行等市場資料模型。

// ---------------------------------------------------------------------------
// Phase 4:市場輔助資料模型(大盤指數歷史/股利行事曆/QFII 持股排行)
// ---------------------------------------------------------------------------
//
// 以下型別對應 stock_rust Data API 三個市場輔助 endpoint 的回應
// (docs/stock-mcp-expanded-tools-plan.md §4.8–§4.10)。三個 endpoint 都是
// 「市場層級」查詢而不是「個股」查詢,因此契約上**沒有 404 語意**——
// 查無資料一律回 200 加空陣列,client 端(apiclient.go)也就不需要像
// 個股 endpoint 那樣把 404 轉成 ErrStockNotFound;404 反而代表部署異常
// (例如路由不存在),必須當成一般錯誤處理。

// IndexPoint 是台股大盤加權指數(TAIEX)單一交易日的資料,對應
// market/index-history endpoint 的 points 陣列元素。
//
// 除了 Date 以外全部使用指標型別:資料庫 NUMERIC 欄位無法安全轉成 JSON
// number 時,伺服器端會輸出 null(計畫 §3.1),MCP 端必須原樣保留這個
// 「缺值」語意,不可用 0 冒充。
type IndexPoint struct {
	// Date 是這筆指數所屬的交易日,格式 "YYYY-MM-DD"。
	Date string `json:"date"`
	// Index 收盤指數(點)。
	Index *float64 `json:"index"`
	// Change 漲跌點數(正數上漲、負數下跌)。
	Change *float64 `json:"change"`
	// TradeValue 成交金額(元)。
	TradeValue *float64 `json:"trade_value"`
	// Transaction 成交筆數。
	Transaction *float64 `json:"transaction"`
	// TradingVolume 成交股數。
	TradingVolume *float64 `json:"trading_volume"`
}

// MarketIndexHistory 是 market/index-history endpoint 的完整回應 envelope。
type MarketIndexHistory struct {
	// DataAsOf 是實際回傳資料中最新一筆的日期("YYYY-MM-DD");查無資料
	// 時為 nil(JSON null),由 stock_rust 伺服器端單一來源決定,MCP 端
	// 不自行重算。
	DataAsOf *string `json:"data_as_of"`
	// Points 依日期由新到舊排序;空結果必須是空陣列而非 null。
	Points []IndexPoint `json:"points"`
}

// DividendEvent 是股利行事曆中的單一事件,對應 market/dividend-calendar
// endpoint 的 events 陣列元素。
//
// 台股領域背景:一筆股利資料(dividend 表的一列)最多有四個日期——
// 除息日、除權日、現金股利發放日、股票股利發放日。行事曆把「一列股利」
// 攤平成「多筆事件」:同一列股利若有多個日期落在查詢區間內,會輸出多筆
// 事件,每筆事件只有一個 EventType 與一個 EventDate。
type DividendEvent struct {
	// StockSymbol 股票代號;Name 股票中文名稱。
	StockSymbol string `json:"stock_symbol"`
	Name        string `json:"name"`
	// EventType 事件類型,值域固定四種:
	//   - "ex_dividend":除息(領現金股利的最後持有基準)。
	//   - "ex_rights":除權(領股票股利的最後持有基準)。
	//   - "cash_payable":現金股利發放日(股東實際收到現金)。
	//   - "stock_payable":股票股利發放日(股東實際收到股票)。
	EventType string `json:"event_type"`
	// EventDate 事件日期,格式 "YYYY-MM-DD"。伺服器端只會輸出可解析的
	// 合法日期(資料庫裡的 "-"、"尚未公布" 等標記不會產生事件)。
	EventDate string `json:"event_date"`
	// DividendYear 股利所屬年度(西元);Quarter 期間標記("A" 年度、
	// "H1"/"H2" 半年度、"Q1"–"Q4" 季度,對映規則同 §3.5)。
	DividendYear int32  `json:"dividend_year"`
	Quarter      string `json:"quarter"`
	// CashDividend 現金股利、StockDividend 股票股利、TotalDividend 合計
	// (元/股);缺值為 nil,不以 0 取代。
	CashDividend  *float64 `json:"cash_dividend"`
	StockDividend *float64 `json:"stock_dividend"`
	TotalDividend *float64 `json:"total_dividend"`
}

// DividendCalendar 是 market/dividend-calendar endpoint 的完整回應 envelope。
type DividendCalendar struct {
	// DataAsOf 依契約(§3.4)固定為 nil:行事曆混合多支股票、多種事件
	// 類型,沒有「單一正確的統計日期」可言——每筆事件各自的日期放在
	// EventDate 裡。這裡仍保留欄位,維持所有 envelope 形狀一致。
	DataAsOf *string `json:"data_as_of"`
	// Events 依事件日期由近到遠(event_date ASC,行事曆語意)排序;
	// 空結果必須是空陣列而非 null。
	Events []DividendEvent `json:"events"`
}

// QfiiHolding 是外資(QFII)持股排行中的單一股票,對應
// market/qfii-holding-ranking endpoint 的 stocks 陣列元素。
//
// 股數欄位(QfiiSharesHeld、IssuedShare)使用 *int64 而非 *float64:
// 台積電等大型股的發行股數超過 250 億股,雖仍在 float64 可精確表示的
// 整數範圍內,但股數在語意上就是整數,用 int64 能明確表達「不會有小數」
// 並避免未來數字更大時的精度疑慮。
type QfiiHolding struct {
	// Rank 名次(由 1 起算,依查詢的 sort_by 指標由高到低)。
	Rank uint32 `json:"rank"`
	// StockSymbol 股票代號;Name 股票中文名稱。
	StockSymbol string `json:"stock_symbol"`
	Name        string `json:"name"`
	// MarketID 市場編號(2 上市、4 上櫃);IndustryID 產業分類編號。
	MarketID   int32 `json:"market_id"`
	IndustryID int32 `json:"industry_id"`
	// QfiiSharesHeld 外資持有股數(股)。
	QfiiSharesHeld *int64 `json:"qfii_shares_held"`
	// QfiiShareHoldingPercentage 外資持股比例(%)。
	QfiiShareHoldingPercentage *float64 `json:"qfii_share_holding_percentage"`
	// IssuedShare 已發行股數(股)。
	IssuedShare *int64 `json:"issued_share"`
}

// QfiiHoldingRanking 是 market/qfii-holding-ranking endpoint 的完整回應
// envelope。
//
// ## 重要限制:這是「當前快照」,不是歷史序列
//
// stock_rust 的 stocks 表只保存**最近一次**排程更新(每日 22:00 UTC)寫入
// 的 QFII 持股數字,舊值會被覆蓋,資料庫裡沒有任何歷史序列——因此這個
// 排行**無法回答「外資最近增持/減持了哪些股票」這類趨勢問題**。
//
// DataAsOf 也因此固定為 nil:stocks 表沒有列級的「QFII 數字更新日期」
// 欄位,契約(§4.10)明確要求不可偽造一個日期;取而代之的是 MCP 摘要
// 必須以文字明確說明「為最近一次每日更新的快照」。
type QfiiHoldingRanking struct {
	DataAsOf *string `json:"data_as_of"`
	// Stocks 依查詢的 sort_by 指標由高到低排序,同值以股票代號由小到大
	// 穩定排序;空結果必須是空陣列而非 null。
	Stocks []QfiiHolding `json:"stocks"`
}

// IndexHistoryOptions 是大盤指數歷史的查詢條件。
//
// 與其他 options 相同的慣例:零值(空字串/0)代表「未提供」——字串
// 日期由 tool 層驗證後傳入,Limit 由 tool 層套用預設值與範圍檢查後
// 保證非零。
type IndexHistoryOptions struct {
	// From / To:日期區間,格式 "YYYY-MM-DD",空字串代表不限制。
	From, To string
	// Limit 最多回傳筆數。
	Limit int
}

// CalendarOptions 是股利行事曆的查詢條件。
type CalendarOptions struct {
	// From / To:事件日期區間,格式 "YYYY-MM-DD"。空字串時由伺服器端
	// 套用預設(From 預設查詢當日、To 預設 From 加 30 天);兩者都提供
	// 時 tool 層已先驗證 From 不晚於 To 且區間不超過 92 天。
	From, To string
	// EventType 事件類型:"ex_dividend"、"ex_rights"、"cash_payable"、
	// "stock_payable" 或 "all";tool 層保證傳入時已套用預設值 "all"。
	EventType string
	// Limit 最多回傳筆數。
	Limit int
}

// QfiiRankingOptions 是 QFII 持股排行的查詢條件。
type QfiiRankingOptions struct {
	// Market 市場範圍:"all"(上市+上櫃)、"twse" 或 "tpex";tool 層
	// 保證傳入時已套用預設值 "all"。
	Market string
	// IndustryID 產業代碼,0 代表未提供,不會放進 Data API query string。
	IndustryID int
	// SortBy 排序指標:"percentage"(持股比例)或 "shares"(持股數);
	// tool 層保證傳入時已套用預設值 "percentage"。
	SortBy string
	// Limit 最多回傳筆數。
	Limit int
}

// MoversOptions 是當日漲跌幅／成交量排行的查詢條件。
//
// 刻意**沒有**「指定資料來源」的欄位:現在是不是盤中,由 stock_rust 依
// 即時報價快取是否為空判斷,MCP 端與呼叫端都不做時鐘判斷(服務重啟、
// 國定假日、颱風停市時時鐘會判斷錯,快取狀態不會)。
type MoversOptions struct {
	// RankBy 排序鍵:"top_gainers"(漲幅)、"top_losers"(跌幅)或
	// "top_volume"(成交量);tool 層保證傳入時已套用預設值 top_gainers。
	RankBy string
	// Market 市場範圍:"all"(上市+上櫃)、"twse" 或 "tpex";tool 層
	// 保證傳入時已套用預設值 "all"。
	Market string
	// Limit 最多回傳筆數。
	Limit int
}

// Mover 是排行中的單一股票。
type Mover struct {
	// Rank 名次(由 1 起算,依查詢的 rank_by 排序)。
	Rank uint32 `json:"rank"`
	// StockSymbol 股票代號;Name 股票中文名稱。
	StockSymbol string `json:"stock_symbol"`
	Name        string `json:"name"`
	// MarketID 市場編號(2 上市、4 上櫃);IndustryID 產業分類編號。
	MarketID   int32 `json:"market_id"`
	IndustryID int32 `json:"industry_id"`
	// Price 成交價:盤中為最新成交價,收盤後為收盤價。
	Price *float64 `json:"price"`
	// Change 漲跌(元);ChangePercent 漲跌幅(%)。
	Change        *float64 `json:"change"`
	ChangePercent *float64 `json:"change_percent"`
	// Open / High / Low 當日開高低價。
	Open *float64 `json:"open"`
	High *float64 `json:"high"`
	Low  *float64 `json:"low"`
	// LastClose 昨收價;僅即時來源有值(日線表沒有這個欄位)。
	LastClose *float64 `json:"last_close"`
	// VolumeLots 成交量(張);僅即時來源有值。
	VolumeLots *float64 `json:"volume_lots"`
	// VolumeShares 成交量(股);僅收盤來源有值。
	VolumeShares *float64 `json:"volume_shares"`
	// TradeValue 成交金額(元);Transaction 成交筆數。兩者僅收盤來源有值。
	TradeValue  *float64 `json:"trade_value"`
	Transaction *float64 `json:"transaction"`
	// SourceSite 即時報價的採集站點(例如 HiStock、Yahoo);僅即時來源有值。
	SourceSite *string `json:"source_site"`
}

// MarketMovers 是 market/movers endpoint 的完整回應 envelope。
//
// ## 為什麼同一個 endpoint 會有兩種資料來源?
//
// 「今天漲最多的是哪幾檔」在盤中與收盤後是同一個問題,但資料分別放在
// 記憶體的即時快照與收盤後的日線表。stock_rust 依即時快取狀態自動選擇
// 來源,並把選擇結果誠實寫在 Source / IsRealtime / DataAsOf 三個欄位。
//
// ## 必須注意的空窗:13:30 收盤到 15:00 之間
//
// 台股 13:30 收盤時即時採集任務停止並清空快取,但當日日線要到 15:00
// 的收盤排程才寫入資料庫。這段時間查詢會拿到 Source = "closing" 且
// DataAsOf 是**前一交易日**的資料——tool 層必須在摘要中明講,不可讓
// 呼叫端把它當成今日行情。
type MarketMovers struct {
	// DataAsOf 資料日期(YYYY-MM-DD):即時來源為今日,收盤來源為該批
	// 日線的實際交易日。與其他工具不同,這個欄位永遠有值。
	DataAsOf string `json:"data_as_of"`
	// Source 資料來源:"realtime"(盤中即時快照)或 "closing"(收盤日線)。
	Source string `json:"source"`
	// IsRealtime 是否為即時資料,等同於 Source == "realtime"。這是所有
	// 工具中唯一可能為 true 的分析型輸出。
	IsRealtime bool `json:"is_realtime"`
	// RankBy / Market 伺服器端實際採用的排序鍵與市場條件。
	RankBy string `json:"rank_by"`
	Market string `json:"market"`
	// SnapshotUpdatedAt 即時快照批次的最新更新時間(UTC ISO 8601);
	// 收盤來源固定為 nil。
	SnapshotUpdatedAt *string `json:"snapshot_updated_at"`
	// Movers 依 RankBy 排序、同值以股票代號由小到大穩定排序的排行;
	// 空結果必須是空陣列而非 null。
	Movers []Mover `json:"movers"`
}

// MarketDataQuerier 是 Phase 4 三個市場輔助工具對資料來源的最小需求介面。
//
// 與 FinancialQuerier/AnalyticsQuerier 相同的設計:介面由消費端
// (tools.go)定義,AddTools 以型別斷言檢查注入的資料來源是否具備這組
// 能力,目前只有 *APIClient 實作。
// 三個方法都回傳完整 Data API envelope(§5.2 實作決策),讓 data_as_of
// 與空陣列語意維持由伺服器端單一來源決定。
type MarketDataQuerier interface {
	MarketIndexHistory(context.Context, IndexHistoryOptions) (*MarketIndexHistory, error)
	DividendCalendar(context.Context, CalendarOptions) (*DividendCalendar, error)
	QfiiHoldingRanking(context.Context, QfiiRankingOptions) (*QfiiHoldingRanking, error)
	// MarketMovers 是後續新增的當日漲跌幅／成交量排行。放進同一個介面
	// 而不是另開第五個介面:它同樣是市場層級查詢、同樣只有 *APIClient
	// 具備這個能力,拆開只會讓註冊邏輯多一層卻不帶來任何彈性。
	MarketMovers(context.Context, MoversOptions) (*MarketMovers, error)
}
