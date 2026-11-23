package stock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 重運算租約（會員系統計畫 2.3「重運算租約規格」）：Data API 路徑含 /backtest 的請求，送出前向 stock_go
// 請領租約（POST /stock/v1/internal/analysis/leases，MCP_QUOTA_TOKEN），以三個標頭帶給 stock_rust。
// 一張租約 = 一次 Data API 請求；member_id 取自 Token 的 Principal，不取自工具參數。

// 帶給 stock_rust 的租約標頭（密鑰只放標頭，不放查詢字串）。
const (
	headerLeaseID     = "X-Analysis-Lease-Id"
	headerLeaseSecret = "X-Analysis-Lease-Secret"
	headerLeaseMember = "X-Analysis-Member-Id"
)

// Lease 是一張已核發的租約。
type Lease struct {
	ID        string    `json:"lease_id"`
	Secret    string    `json:"lease_secret"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LeaseIssuer 向 stock_go 請領租約。
type LeaseIssuer interface {
	IssueLease(ctx context.Context, memberID int64, endpoint string) (Lease, error)
}

// LeaseError 是可辨識的租約失敗；工具回給模型的訊息由 Message 產生，不含內部細節。
type LeaseError struct {
	// Reason：daily（今日次數用完）、concurrent（同時執行已滿）、unconfigured（未設定配額）、
	// forbidden（沒有 analysis.run）、no_member（金鑰沒有對應會員）、unavailable（stock_go 無法使用）。
	Reason     string
	RetryAfter time.Duration
}

func (e *LeaseError) Error() string { return "analysis lease: " + e.Reason }

// Message 是給模型看的說明。
func (e *LeaseError) Message() string {
	switch e.Reason {
	case "daily":
		return fmt.Sprintf("今日回測次數已用完（重運算配額），請於 %s 後再試。", retryText(e.RetryAfter))
	case "concurrent":
		return fmt.Sprintf("同時執行的回測已達上限，請於 %s 後再試。", retryText(e.RetryAfter))
	case "unconfigured":
		return "回測配額尚未設定，目前無法執行回測，請聯絡管理員。"
	case "forbidden":
		return "此帳號沒有執行回測（analysis.run）的權限。"
	case "no_member":
		return "此金鑰沒有對應的會員，無法執行回測。"
	default:
		return "目前無法取得回測配額，請稍後再試。"
	}
}

func retryText(d time.Duration) string {
	switch {
	case d <= 0:
		return "稍後"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分鐘", int(d.Round(time.Minute).Minutes()))
	default:
		return fmt.Sprintf("約 %d 小時", int(d.Round(time.Hour).Hours()))
	}
}

// StockGoLeases 經 stock_go 的內部端點請領租約。
type StockGoLeases struct {
	baseURL, token string
	client         *http.Client
}

// NewStockGoLeases 建立租約請領 client；baseURL 是 stock_go 的 API 位址，token 是 MCP_QUOTA_TOKEN。
func NewStockGoLeases(baseURL, token string, timeout time.Duration) (*StockGoLeases, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || token == "" {
		return nil, errors.New("租約請領需要 stock_go 位址與 MCP_QUOTA_TOKEN")
	}
	return &StockGoLeases{baseURL: baseURL, token: token, client: &http.Client{Timeout: timeout}}, nil
}

// IssueLease 請領一張租約；每次操作產生新的 request_id（UUID v7）。
func (l *StockGoLeases) IssueLease(ctx context.Context, memberID int64, endpoint string) (Lease, error) {
	requestID, err := uuid.NewV7()
	if err != nil {
		return Lease{}, fmt.Errorf("產生 request_id：%w", err)
	}
	payload, err := json.Marshal(map[string]any{"member_id": memberID, "request_id": requestID.String(), "endpoint": endpoint})
	if err != nil {
		return Lease{}, fmt.Errorf("序列化租約請求：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/stock/v1/internal/analysis/leases", bytes.NewReader(payload))
	if err != nil {
		return Lease{}, fmt.Errorf("建立租約請求：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return Lease{}, &LeaseError{Reason: "unavailable"}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()
	var body struct {
		Lease
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		if body.ID == "" || body.Secret == "" {
			return Lease{}, &LeaseError{Reason: "unavailable"}
		}
		return body.Lease, nil
	case http.StatusTooManyRequests:
		retry, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return Lease{}, &LeaseError{Reason: body.Reason, RetryAfter: time.Duration(retry) * time.Second}
	case http.StatusForbidden:
		return Lease{}, &LeaseError{Reason: "forbidden"}
	default:
		return Lease{}, &LeaseError{Reason: "unavailable"}
	}
}

// backtestEndpoint 把 Data API 路徑轉成租約登記的端點（去掉 /api/v1 與查詢字串）。
func backtestEndpoint(path string) string {
	path, _, _ = strings.Cut(path, "?")
	return strings.TrimPrefix(path, "/api/v1")
}

// leaseHeaders 依呼叫端身分請領租約，回傳要帶給 stock_rust 的標頭；未設定請領 client 時回 nil（相容期，
// stock_rust 的 DATA_API_STRICT_SCOPE=false 照舊放行）。
func (c *APIClient) leaseHeaders(ctx context.Context, path string) (http.Header, error) {
	if c.leases == nil {
		return nil, nil
	}
	principal, ok := principalFrom(ctx)
	if !ok || principal.MemberID <= 0 {
		return nil, &LeaseError{Reason: "no_member"}
	}
	lease, err := c.leases.IssueLease(ctx, principal.MemberID, backtestEndpoint(path))
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set(headerLeaseID, lease.ID)
	header.Set(headerLeaseSecret, lease.Secret)
	header.Set(headerLeaseMember, strconv.FormatInt(principal.MemberID, 10))
	return header, nil
}
