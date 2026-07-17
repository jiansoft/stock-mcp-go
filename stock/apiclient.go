// Package stock 的 API client 實作讓 MCP 在不持有資料庫憑證的情況下，透過
// stock_rust 的版本化 Data API 取得與 Repository 相同的唯讀資料。
package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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
func (c *APIClient) StockProfile(ctx context.Context, symbol string) (*StockProfile, error) {
	var body StockProfile
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
func (c *APIClient) StockValuation(ctx context.Context, symbol string, opt ValuationOptions) (*StockValuationEnvelope, error) {
	values := url.Values{}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	path := "/api/v1/stocks/" + url.PathEscape(symbol) + "/valuation"
	if query := values.Encode(); query != "" {
		path += "?" + query
	}
	var body StockValuationEnvelope
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
func (c *APIClient) ScreenStocks(ctx context.Context, opt ScreenOptions) (*StockScreening, error) {
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

	var body StockScreening
	if err := c.get(ctx, "/api/v1/stocks/screen?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Stocks == nil {
		body.Stocks = []ScreenedStock{}
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
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("資料 API 回傳非成功狀態:%d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return false, fmt.Errorf("解析資料 API 回應:%w", err)
	}
	return true, nil
}
