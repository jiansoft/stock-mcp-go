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
