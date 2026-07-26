package stock

// 本檔案是 Phase 4:市場輔助工具的完整實作:輸入型別、JSON Schema、
// 驗證輔助函式、structuredContent 輸出型別與 handler。
//
// 這些工具由 tools.go 的 AddTools 依「資料來源是否具備對應能力介面」
// 動態註冊;共用的驗證與輸出輔助函式(normalizeSymbol、rangedLimit、
// textResult、displayFloat 等)也都定義在 tools.go。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Phase 4:市場輔助工具(大盤指數歷史/股利行事曆/QFII 持股排行)
// ---------------------------------------------------------------------------
//
// 三個工具都是「市場層級」查詢,沒有個股 404 語意:合法條件查無資料時
// Data API 回 200 空陣列,工具誠實說明「沒有資料」而不是回錯誤;任何
// 非 2xx(含 404)都由 client 轉成一般 error,這裡記 log 後回安全通用
// 訊息,錯誤分層原則與其他 Phase 一致。

// marketDataToolset 綁定 MarketDataQuerier 與安全記錄函式,原則同
// financialToolset/analyticsToolset:不擴充既有介面,為新能力建立獨立
// 小介面與獨立 toolset。
type marketDataToolset struct {
	marketData MarketDataQuerier
	logf       func(string, ...any)
}

// IndexHistoryInput 是 get_market_index_history 的輸入參數型別。
type IndexHistoryInput struct {
	// From / To 日期區間(格式 YYYY-MM-DD,選填);Limit 筆數上限(選填,
	// 預設 30)。零值(空字串/0)代表呼叫端沒有提供,由 handler 套用預設。
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// DividendCalendarInput 是 get_dividend_calendar 的輸入參數型別。
type DividendCalendarInput struct {
	// From / To 事件日期區間(選填;未提供時由伺服器端預設「查詢當日起
	// 30 天」);EventType 事件類型(選填,預設 all)。
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// QfiiRankingInput 是 get_qfii_holding_ranking 的輸入參數型別。
type QfiiRankingInput struct {
	Market     string `json:"market,omitempty"`
	IndustryID int    `json:"industry_id,omitempty"`
	// SortBy 排序指標(選填,預設 percentage)。
	SortBy string `json:"sort_by,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// indexHistorySchema 描述 get_market_index_history 的輸入形狀。
// 與 get_price_history 慣例一致:limit 預設 30、範圍 1–365。
func indexHistorySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
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
	}
}

// dividendCalendarSchema 描述 get_dividend_calendar 的輸入形狀。
//
// 「from 到 to 不可超過 92 天」是兩個欄位之間的關聯限制,JSON Schema
// 無法表達,由 handler 的 validateCalendarRange 在程式碼裡檢查——
// Data API 也會再驗一次(回 422),MCP 層先驗可以在送出請求前就給
// 呼叫端明確的修正指引。
func dividendCalendarSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"from": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "起始日期(格式 YYYY-MM-DD,選填;未提供時預設查詢當日)",
			},
			"to": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "結束日期(格式 YYYY-MM-DD,選填;未提供時預設起始日期加 30 天,區間最長 92 天)",
			},
			"event_type": {
				Type:        "string",
				Enum:        []any{"ex_dividend", "ex_rights", "cash_payable", "stock_payable", "all"},
				Default:     []byte(`"all"`),
				Description: "事件類型:ex_dividend(除息)、ex_rights(除權)、cash_payable(現金股利發放)、stock_payable(股票股利發放)或 all(全部)",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(200.0),
				Default:     []byte("50"),
				Description: "最大回傳筆數(預設 50,範圍 1 至 200)",
			},
		},
	}
}

// qfiiRankingSchema 描述 get_qfii_holding_ranking 的輸入形狀。
// market 沿用排行/選股共用的 listedOTCMarketSchema(all = 上市+上櫃,
// 不含公開發行與興櫃),因為 QFII 排行同樣是直接過濾 stocks 表。
func qfiiRankingSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"market": listedOTCMarketSchema(),
			"industry_id": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Description: "產業代碼(正整數,選填;未知代碼回空陣列)",
			},
			"sort_by": {
				Type:        "string",
				Enum:        []any{"percentage", "shares"},
				Default:     []byte(`"percentage"`),
				Description: "排序指標:percentage(外資持股比例)或 shares(外資持股數),一律由高到低",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(50.0),
				Default:     []byte("20"),
				Description: "最大回傳筆數(預設 20,範圍 1 至 50)",
			},
		},
	}
}

// MarketIndexHistoryOutput 是 get_market_index_history 的 structuredContent:
// 依計畫 §3.4,在 Data API envelope(data_as_of、points)之外加上
// data_kind、is_realtime、disclaimer 三個共通欄位,不改名、不重排內層資料。
type MarketIndexHistoryOutput struct {
	DataKind   string       `json:"data_kind"`
	IsRealtime bool         `json:"is_realtime"`
	Disclaimer string       `json:"disclaimer"`
	DataAsOf   *string      `json:"data_as_of"`
	Points     []IndexPoint `json:"points"`
}

// DividendCalendarOutput 是 get_dividend_calendar 的 structuredContent。
// DataAsOf 依契約固定為 null(混合事件沒有單一統計日期,各事件日期在
// events 內),仍保留欄位讓所有分析型輸出形狀一致。
type DividendCalendarOutput struct {
	DataKind   string          `json:"data_kind"`
	IsRealtime bool            `json:"is_realtime"`
	Disclaimer string          `json:"disclaimer"`
	DataAsOf   *string         `json:"data_as_of"`
	Events     []DividendEvent `json:"events"`
}

// QfiiRankingOutput 是 get_qfii_holding_ranking 的 structuredContent。
// DataAsOf 依契約固定為 null:stocks 表沒有列級的 QFII 更新日期,不可
// 偽造;快照語意改由摘要文字明確說明(§4.10 硬性要求)。
type QfiiRankingOutput struct {
	DataKind   string        `json:"data_kind"`
	IsRealtime bool          `json:"is_realtime"`
	Disclaimer string        `json:"disclaimer"`
	DataAsOf   *string       `json:"data_as_of"`
	Stocks     []QfiiHolding `json:"stocks"`
}

// qfiiSnapshotNotice 是 QFII 排行摘要固定附帶的快照限制說明(§4.10):
// 集中成常數,確保「有資料」與「查無資料」兩種摘要用字一致,測試也
// 只需針對同一份文字驗證。
const qfiiSnapshotNotice = "本排行為最近一次每日更新的快照,無歷史序列,無法回答外資增減持趨勢。"

// eventTypeLabels 把股利行事曆事件類型代碼對映成摘要用的繁體中文名稱。
// structuredContent 內維持英文代碼(契約欄位不翻譯),只有給人讀的
// 摘要文字使用中文。
var eventTypeLabels = map[string]string{
	"ex_dividend":   "除息",
	"ex_rights":     "除權",
	"cash_payable":  "現金股利發放",
	"stock_payable": "股票股利發放",
}

// displayInt64 把 *int64 格式化成摘要文字:nil 顯示「無」,原則同
// displayFloat——只影響給人讀的摘要,structuredContent 仍維持指標語意。
func displayInt64(v *int64) string {
	if v == nil {
		return "無"
	}
	return strconv.FormatInt(*v, 10)
}

// validateCalendarRange 驗證股利行事曆的日期區間:兩者都提供時 from 不可
// 晚於 to,且區間不可超過 92 天(約一季,計畫 §4.9 用來防止全表匯出)。
// 只提供其中一個或都不提供時不檢查,交由伺服器端套用預設區間。
func validateCalendarRange(from, to *time.Time) error {
	if from == nil || to == nil {
		return nil
	}
	if from.After(*to) {
		return fmt.Errorf("起始日期 (from) 不可晚於結束日期 (to)")
	}
	// 92 天是「一季」的上限:除權息旺季(6–9 月)單月事件就可能數百筆,
	// 更長的區間對行事曆查詢沒有意義,只會變成全表匯出。
	if to.Sub(*from) > 92*24*time.Hour {
		return fmt.Errorf("查詢區間 (from 到 to) 不可超過 92 天")
	}
	return nil
}

// marketIndexHistory 執行 get_market_index_history:驗證輸入 → 呼叫
// Data API → 組出摘要與結構化輸出。
//
// MCP protocol-level error(例如不存在的工具)由 SDK 處理;本 handler
// 回傳的 Go error 會成為 tool-level error。日期與筆數的輸入錯誤可安全
// 原樣告知使用者;API 認證、timeout、5xx、404(路由異常)或解析錯誤
// 只寫 server log,對外一律回安全通用訊息。
func (ts *marketDataToolset) marketIndexHistory(ctx context.Context, _ *mcp.CallToolRequest, in IndexHistoryInput) (*mcp.CallToolResult, any, error) {
	from, err := parseOptionalDate(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseOptionalDate(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	// YYYY-MM-DD 是零填充的固定寬度格式,字典序即時間序,兩者都有提供
	// 時直接比較字串即可判斷先後。
	if from != "" && to != "" && from > to {
		return nil, nil, fmt.Errorf("起始日期 (from) 不可晚於結束日期 (to)")
	}
	limit, err := rangedLimit(in.Limit, 30, 1, 365)
	if err != nil {
		return nil, nil, err
	}

	envelope, err := ts.marketData.MarketIndexHistory(ctx, IndexHistoryOptions{From: from, To: to, Limit: limit})
	if err != nil {
		ts.logf("工具 get_market_index_history 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定範圍內沒有台股大盤指數資料。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Points) > 0 {
		// 指數依日期新到舊排序,第一筆就是最新一筆,直接取用組摘要。
		latest := envelope.Points[0]
		summary = fmt.Sprintf("取得台股大盤 TAIEX 指數共 %d 筆歷史資料(最新 %s:收盤指數 %s,漲跌 %s 點)。\n免責聲明:%s",
			len(envelope.Points), latest.Date, displayFloat(latest.Index), displayFloat(latest.Change), AnalysisDisclaimer)
	}
	return textResult(summary), MarketIndexHistoryOutput{
		DataKind: "market_index_history", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Points: envelope.Points,
	}, nil
}

// dividendCalendar 執行 get_dividend_calendar。
//
// 「區間不可超過 92 天」在 MCP 層先驗(而不是只靠 Data API 回 422):
// 呼叫端能在送出請求前就得到明確的修正指引,也避免一次必然失敗的
// HTTP 往返。錯誤分層原則同 marketIndexHistory。
func (ts *marketDataToolset) dividendCalendar(ctx context.Context, _ *mcp.CallToolRequest, in DividendCalendarInput) (*mcp.CallToolResult, any, error) {
	from, err := parseDateArg(in.From, "from")
	if err != nil {
		return nil, nil, err
	}
	to, err := parseDateArg(in.To, "to")
	if err != nil {
		return nil, nil, err
	}
	if err := validateCalendarRange(from, to); err != nil {
		return nil, nil, err
	}
	// 空字串代表未提供,套用預設 all;非法值直接報錯,不猜測。
	eventType := strings.ToLower(strings.TrimSpace(in.EventType))
	if eventType == "" {
		eventType = "all"
	}
	if _, ok := eventTypeLabels[eventType]; !ok && eventType != "all" {
		return nil, nil, fmt.Errorf("參數 event_type 必須為 ex_dividend、ex_rights、cash_payable、stock_payable 或 all,收到了 %q", in.EventType)
	}
	limit, err := rangedLimit(in.Limit, 50, 1, 200)
	if err != nil {
		return nil, nil, err
	}

	opt := CalendarOptions{EventType: eventType, Limit: limit}
	// 驗證通過的日期以標準格式傳給 client;nil(未提供)維持空字串,
	// 讓伺服器端套用預設區間。
	if from != nil {
		opt.From = from.Format("2006-01-02")
	}
	if to != nil {
		opt.To = to.Format("2006-01-02")
	}
	envelope, err := ts.marketData.DividendCalendar(ctx, opt)
	if err != nil {
		ts.logf("工具 get_dividend_calendar 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("查詢區間內沒有股利行事曆事件。\n免責聲明:%s", AnalysisDisclaimer)
	if len(envelope.Events) > 0 {
		// 行事曆依事件日期由近到遠(ASC)排序,第一筆就是最接近的事件。
		first := envelope.Events[0]
		summary = fmt.Sprintf("查詢區間內共 %d 筆股利行事曆事件;最近一筆為 %s 的 %s %s(%s),現金股利 %s 元、股票股利 %s 元。\n免責聲明:%s",
			len(envelope.Events), first.EventDate, first.StockSymbol, first.Name, eventTypeLabels[first.EventType],
			displayFloat(first.CashDividend), displayFloat(first.StockDividend), AnalysisDisclaimer)
	}
	return textResult(summary), DividendCalendarOutput{
		DataKind: "dividend_calendar", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Events: envelope.Events,
	}, nil
}

// qfiiHoldingRanking 執行 get_qfii_holding_ranking。
//
// §4.10 硬性要求:不論有無資料,摘要都必須明確標示「為最近一次每日
// 更新的快照,無歷史序列」,避免 LLM 把單一快照誤答成「外資最近增減持
// 趨勢」。錯誤分層原則同 marketIndexHistory。
func (ts *marketDataToolset) qfiiHoldingRanking(ctx context.Context, _ *mcp.CallToolRequest, in QfiiRankingInput) (*mcp.CallToolResult, any, error) {
	market, err := normalizeMarket(in.Market)
	if err != nil {
		return nil, nil, err
	}
	if in.IndustryID < 0 {
		return nil, nil, fmt.Errorf("參數 industry_id 必須是正整數")
	}
	// 空字串代表未提供,套用預設 percentage;非法值直接報錯,不猜測。
	sortBy := strings.ToLower(strings.TrimSpace(in.SortBy))
	if sortBy == "" {
		sortBy = "percentage"
	}
	if sortBy != "percentage" && sortBy != "shares" {
		return nil, nil, fmt.Errorf("參數 sort_by 必須為 percentage 或 shares,收到了 %q", in.SortBy)
	}
	limit, err := rangedLimit(in.Limit, 20, 1, 50)
	if err != nil {
		return nil, nil, err
	}

	envelope, err := ts.marketData.QfiiHoldingRanking(ctx, QfiiRankingOptions{
		Market: market, IndustryID: in.IndustryID, SortBy: sortBy, Limit: limit,
	})
	if err != nil {
		ts.logf("工具 get_qfii_holding_ranking 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}

	summary := fmt.Sprintf("指定條件下沒有外資持股排行資料。%s\n免責聲明:%s", qfiiSnapshotNotice, AnalysisDisclaimer)
	if len(envelope.Stocks) > 0 {
		top := envelope.Stocks[0]
		summary = fmt.Sprintf("取得 %s 市場外資(QFII)持股排行共 %d 檔(依 %s 排序);第 1 名為 %s %s,外資持股比例 %s%%、持股數 %s 股。%s\n免責聲明:%s",
			market, len(envelope.Stocks), sortBy, top.StockSymbol, top.Name,
			displayFloat(top.QfiiShareHoldingPercentage), displayInt64(top.QfiiSharesHeld), qfiiSnapshotNotice, AnalysisDisclaimer)
	}
	return textResult(summary), QfiiRankingOutput{
		DataKind: "qfii_holding_ranking", IsRealtime: false, Disclaimer: AnalysisDisclaimer,
		DataAsOf: envelope.DataAsOf, Stocks: envelope.Stocks,
	}, nil
}
