// Package stock 的 API client 實作讓 MCP 在不持有資料庫憑證的情況下，透過
// stock_rust 的版本化 Data API 取得與 Repository 相同的唯讀資料。
package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// maxAPIResponseBytes 是單一 Data API 回應允許解析的上限(32 MiB)。
	// 正常回應遠小於此值,這只是防止對端異常時無上限地吃記憶體。
	maxAPIResponseBytes = 32 << 20
	// maxDrainBytes 是關閉連線前願意丟棄讀取的 body 上限(64 KiB),用來
	// 換取底層 TCP 連線可以被重複使用。
	maxDrainBytes = 64 << 10
)

// APIClient 是 stock_rust Data API 的 HTTP 實作；不記錄 API key，並以明確 timeout 避免內網故障時卡住 MCP worker。
type APIClient struct {
	baseURL, apiKey string
	client          *http.Client
}

// NewAPIClient 建立 Data API client；baseURL 必須是可到達的內網服務根位址。
func NewAPIClient(baseURL, apiKey string, timeout time.Duration) *APIClient {
	return &APIClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: &http.Client{Timeout: timeout}}
}

// SearchStock 查詢符合關鍵字的股票清單。
func (c *APIClient) SearchStock(ctx context.Context, query string, limit int) ([]Stock, error) {
	var body struct {
		Stocks []Stock `json:"stocks"`
	}
	if err := c.get(ctx, "/api/v1/stocks/search?query="+url.QueryEscape(query)+fmt.Sprintf("&limit=%d", limit), &body); err != nil {
		return nil, err
	}
	return body.Stocks, nil
}

// LatestDailyQuote 查詢股票與可選最新日報價；404 代表股票不存在。
func (c *APIClient) LatestDailyQuote(ctx context.Context, symbol string) (*LatestDailyQuote, error) {
	var body LatestDailyQuote
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/latest-quote", &body)
	if err != nil || !found {
		return nil, err
	}
	return &body, nil
}

// PriceHistory 查詢歷史日線；404 轉為 ErrStockNotFound 以維持 db/api 模式語意一致。
func (c *APIClient) PriceHistory(ctx context.Context, symbol string, from, to *time.Time, limit int) ([]HistoricalQuote, error) {
	values := url.Values{"limit": []string{fmt.Sprint(limit)}}
	if from != nil {
		values.Set("from", from.Format("2006-01-02"))
	}
	if to != nil {
		values.Set("to", to.Format("2006-01-02"))
	}
	var body struct {
		Quotes []HistoricalQuote `json:"quotes"`
	}
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/price-history?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return body.Quotes, nil
}

// StockProfile 查詢完整股票 profile；404 代表股票不存在。
func (c *APIClient) StockProfile(ctx context.Context, symbol string) (*Profile, error) {
	var body Profile
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/profile", &body)
	if err != nil || !found {
		return nil, err
	}
	return &body, nil
}

// RealtimeSnapshot 查詢近即時快照；404 會保留為 nil，讓工具可顯示 API 區分過的安全訊息。
func (c *APIClient) RealtimeSnapshot(ctx context.Context, symbol string) (*RealtimeSnapshot, error) {
	var body RealtimeSnapshot
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/realtime-snapshot", &body)
	if err != nil || !found {
		return nil, err
	}
	return &body, nil
}

// MonthlyRevenueHistory 查詢月營收歷史;404 轉為 ErrStockNotFound。
//
// 回傳完整 envelope(而非只有內層清單),讓 data_as_of 維持「由
// stock_rust 伺服器端單一來源決定」的原則,MCP 端不自行重算。
func (c *APIClient) MonthlyRevenueHistory(ctx context.Context, symbol string, opt RevenueHistoryOptions) (*MonthlyRevenueHistory, error) {
	values := url.Values{"limit": []string{fmt.Sprint(opt.Limit)}}
	if opt.From != "" {
		values.Set("from", opt.From)
	}
	if opt.To != "" {
		values.Set("to", opt.To)
	}
	var body MonthlyRevenueHistory
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/monthly-revenues?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	// 防禦性保證:即使伺服器端序列化出 null,對外也永遠是空陣列——
	// 「查無資料」在 JSON 輸出必須是 [] 而非 null(計畫 §3.4)。
	if body.Revenues == nil {
		body.Revenues = []MonthlyRevenue{}
	}
	return &body, nil
}

// FinancialStatementHistory 查詢季/年度財報歷史;404 轉為 ErrStockNotFound。
func (c *APIClient) FinancialStatementHistory(ctx context.Context, symbol string, opt StatementHistoryOptions) (*FinancialStatementHistory, error) {
	values := url.Values{"limit": []string{fmt.Sprint(opt.Limit)}}
	if opt.PeriodType != "" {
		values.Set("period_type", opt.PeriodType)
	}
	var body FinancialStatementHistory
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/financial-statements?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Statements == nil {
		body.Statements = []FinancialStatement{}
	}
	return &body, nil
}

// DividendHistory 查詢股利發放歷史;404 轉為 ErrStockNotFound。
func (c *APIClient) DividendHistory(ctx context.Context, symbol string, opt DividendHistoryOptions) (*DividendHistory, error) {
	values := url.Values{"limit": []string{fmt.Sprint(opt.Limit)}}
	// FromYear/ToYear 的 0 值代表「未提供」,不放進 query string,讓
	// 伺服器端自行套用「不限制年度」的預設語意。
	if opt.FromYear != 0 {
		values.Set("from_year", fmt.Sprint(opt.FromYear))
	}
	if opt.ToYear != 0 {
		values.Set("to_year", fmt.Sprint(opt.ToYear))
	}
	var body DividendHistory
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/dividends?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	if body.Dividends == nil {
		body.Dividends = []Dividend{}
	}
	return &body, nil
}

// StockValuation 查詢個股估值並回傳完整 envelope；404 只代表股票不存在。
// 股票存在但 31 天回溯窗口內沒有估值時，Data API 會以 200 回傳
// valuation:null，client 會原樣保留而不把它誤判成錯誤。
func (c *APIClient) StockValuation(ctx context.Context, symbol string, opt ValuationOptions) (*ValuationEnvelope, error) {
	values := url.Values{}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	path := "/api/v1/stocks/" + url.PathEscape(symbol) + "/valuation"
	if query := values.Encode(); query != "" {
		path += "?" + query
	}
	var body ValuationEnvelope
	found, err := c.getFound(ctx, path, &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// MarketBreadth 查詢市場廣度序列並保留伺服器端決定的 data_as_of。
func (c *APIClient) MarketBreadth(ctx context.Context, opt MarketBreadthOptions) (*MarketBreadth, error) {
	values := url.Values{
		"market": []string{opt.Market},
		"days":   []string{fmt.Sprint(opt.Days)},
	}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	var body MarketBreadth
	found, err := c.getFound(ctx, "/api/v1/market/breadth?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	if body.History == nil {
		body.History = []MarketBreadthPoint{}
	}
	return &body, nil
}

// DividendYieldRanking 查詢殖利率排行並回傳完整 envelope。
// market=all 的「上市加上櫃、不含興櫃」語意由 Data API 統一實作，
// client 只傳固定 enum，避免兩端各自維護市場 id 對映。
func (c *APIClient) DividendYieldRanking(ctx context.Context, opt YieldRankingOptions) (*DividendYieldRanking, error) {
	values := url.Values{
		"market": []string{opt.Market},
		"limit":  []string{fmt.Sprint(opt.Limit)},
	}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	if opt.IndustryID != 0 {
		values.Set("industry_id", fmt.Sprint(opt.IndustryID))
	}
	var body DividendYieldRanking
	found, err := c.getFound(ctx, "/api/v1/market/dividend-yield-ranking?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMarketDataNotFound
	}
	if body.Stocks == nil {
		body.Stocks = []YieldRank{}
	}
	return &body, nil
}

// ScreenStocks 以固定白名單 query parameters 查詢條件選股，並回傳完整
// envelope。浮點門檻只有非 nil 時才送出，因為 0 是合法條件，不能用
// Go 零值把「未提供」與「門檻為 0」混為一談。
func (c *APIClient) ScreenStocks(ctx context.Context, opt ScreenOptions) (*Screening, error) {
	values := url.Values{
		"market":     []string{opt.Market},
		"sort_by":    []string{opt.SortBy},
		"sort_order": []string{opt.SortOrder},
		"limit":      []string{fmt.Sprint(opt.Limit)},
	}
	if opt.IndustryID != 0 {
		values.Set("industry_id", fmt.Sprint(opt.IndustryID))
	}
	if opt.ValuationBand != "" {
		values.Set("valuation_band", opt.ValuationBand)
	}
	if opt.MinRevenueYOYPercent != nil {
		values.Set("min_revenue_yoy_percent", formatFloat(*opt.MinRevenueYOYPercent))
	}
	if opt.MinEPS != nil {
		values.Set("min_eps", formatFloat(*opt.MinEPS))
	}
	if opt.MinROEPercent != nil {
		values.Set("min_roe_percent", formatFloat(*opt.MinROEPercent))
	}
	if opt.MinDividendYieldPercent != nil {
		values.Set("min_dividend_yield_percent", formatFloat(*opt.MinDividendYieldPercent))
	}

	var body Screening
	if err := c.get(ctx, "/api/v1/stocks/screen?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Stocks == nil {
		body.Stocks = []ScreenedStock{}
	}
	return &body, nil
}

// MarketIndexHistory 查詢台股大盤 TAIEX 指數歷史並回傳完整 envelope。
//
// 這是市場層級 endpoint,契約上沒有 404 語意(查無資料回 200 空陣列),
// 因此使用 c.get:404 反而代表路由不存在等部署異常,必須是一般 error,
// 交由 tool 層記 log 並回安全通用訊息。
func (c *APIClient) MarketIndexHistory(ctx context.Context, opt IndexHistoryOptions) (*MarketIndexHistory, error) {
	values := url.Values{"limit": []string{fmt.Sprint(opt.Limit)}}
	// From/To 空字串代表「未提供」,不放進 query string,讓伺服器端套用
	// 「不限制區間」的預設語意。
	if opt.From != "" {
		values.Set("from", opt.From)
	}
	if opt.To != "" {
		values.Set("to", opt.To)
	}
	var body MarketIndexHistory
	if err := c.get(ctx, "/api/v1/market/index-history?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	// 防禦性保證:即使伺服器端序列化出 null,對外也永遠是空陣列——
	// 「查無資料」在 JSON 輸出必須是 [] 而非 null(計畫 §3.4)。
	if body.Points == nil {
		body.Points = []IndexPoint{}
	}
	return &body, nil
}

// DividendCalendar 查詢日期區間內的除權息與股利發放行事曆,回傳完整
// envelope。同為市場層級 endpoint,404 即異常,使用 c.get。
func (c *APIClient) DividendCalendar(ctx context.Context, opt CalendarOptions) (*DividendCalendar, error) {
	// EventType 由 tool 層保證已套用預設值 "all",固定送出;From/To 空
	// 字串代表未提供,交由伺服器端套用「查詢當日起 30 天」的預設區間。
	values := url.Values{
		"event_type": []string{opt.EventType},
		"limit":      []string{fmt.Sprint(opt.Limit)},
	}
	if opt.From != "" {
		values.Set("from", opt.From)
	}
	if opt.To != "" {
		values.Set("to", opt.To)
	}
	var body DividendCalendar
	if err := c.get(ctx, "/api/v1/market/dividend-calendar?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Events == nil {
		body.Events = []DividendEvent{}
	}
	return &body, nil
}

// QfiiHoldingRanking 查詢外資(QFII)持股排行,回傳完整 envelope。
//
// 這是 stocks 表最近一次每日更新的「當前快照」,沒有歷史序列;契約
// (§4.10)因此規定 data_as_of 固定為 null,client 原樣保留,由 tool 層
// 以文字說明快照語意。市場層級 endpoint,404 即異常,使用 c.get。
func (c *APIClient) QfiiHoldingRanking(ctx context.Context, opt QfiiRankingOptions) (*QfiiHoldingRanking, error) {
	// Market/SortBy 由 tool 層保證已套用預設值,固定送出;IndustryID 的
	// 0 值代表未提供,不放進 query string(產業代碼從 1 起算,0 不是
	// 合法代碼,不會與真實條件混淆)。
	values := url.Values{
		"market":  []string{opt.Market},
		"sort_by": []string{opt.SortBy},
		"limit":   []string{fmt.Sprint(opt.Limit)},
	}
	if opt.IndustryID != 0 {
		values.Set("industry_id", fmt.Sprint(opt.IndustryID))
	}
	var body QfiiHoldingRanking
	if err := c.get(ctx, "/api/v1/market/qfii-holding-ranking?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Stocks == nil {
		body.Stocks = []QfiiHolding{}
	}
	return &body, nil
}

// MarketMovers 查詢當日漲跌幅／成交量排行,回傳完整 envelope。
//
// 資料來源(盤中即時 / 收盤日線)由 stock_rust 依即時快取狀態決定,
// client 不傳任何來源參數,也不改寫伺服器回報的 source / is_realtime /
// data_as_of——這三個欄位是呼叫端判斷「手上這份資料是什麼」的唯一依據,
// 任何一端擅自推斷都會造成前一交易日資料被當成今日行情。
//
// 市場層級 endpoint 沒有個股 404 語意:合法條件查無資料時 Data API 回
// 200 空陣列,只有整張日線表都沒有資料(部署異常)才會 404,因此使用
// c.get 讓 404 直接成為錯誤。
func (c *APIClient) MarketMovers(ctx context.Context, opt MoversOptions) (*MarketMovers, error) {
	// RankBy/Market 由 tool 層保證已套用預設值,固定送出。
	values := url.Values{
		"rank_by": []string{opt.RankBy},
		"market":  []string{opt.Market},
		"limit":   []string{fmt.Sprint(opt.Limit)},
	}
	var body MarketMovers
	if err := c.get(ctx, "/api/v1/market/movers?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	// 防禦性保證:「查無資料」在 JSON 輸出必須是 [] 而非 null(§3.4)。
	if body.Movers == nil {
		body.Movers = []Mover{}
	}
	return &body, nil
}

// get 執行成功必為 200 的 GET 請求。
func (c *APIClient) get(ctx context.Context, path string, output any) error {
	found, err := c.getFound(ctx, path, output)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("資料 API 回傳 404")
	}
	return nil
}

// getFound 將 404 交給呼叫端解讀，其餘非 2xx 狀態都維持安全的通用錯誤。
func (c *APIClient) getFound(ctx context.Context, path string, output any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("建立資料 API 請求:%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("呼叫資料 API:%w", err)
	}
	defer func() {
		// 關閉之前先把還沒讀完的 body 丟棄。Go 的 http.Transport 只有在
		// response body 被「完整讀到 EOF」之後,才會把底層 TCP 連線放回
		// 連線池重複使用;直接 Close 一個沒讀完的 body 會讓連線被丟棄,
		// 下一次請求就得重新建立連線(TCP 三次握手 + 可能的 TLS 交握)。
		//
		// 這件事在 404 與非 2xx 這兩條路徑上特別重要:它們完全沒有讀取
		// body 就直接返回,而查無資料(404)在正常使用中相當常見,若不
		// drain,等於每一次查無資料都白白丟掉一條內網連線。
		//
		// 用 io.LimitReader 包一層當作保險:萬一對端回傳一個異常巨大的
		// 錯誤頁面,drain 的成本也有上限,不會為了重用連線反而卡住。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("資料 API 回傳非成功狀態:%d", resp.StatusCode)
	}
	// 以 io.LimitReader 限制單一回應的最大解析量。Data API 是內網的受信任
	// 服務,正常回應(即使是 365 筆日線或 200 筆行事曆事件)也只有數百 KB,
	// 離上限還很遠;這一層防的是「對端故障或被取代成惡意服務」時,一份
	// 無上限的回應把 MCP server 的記憶體吃光——不信任任何外部輸入的大小,
	// 即使那個外部是自己人的服務。
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponseBytes)).Decode(output); err != nil {
		return false, fmt.Errorf("解析資料 API 回應:%w", err)
	}
	return true, nil
}
