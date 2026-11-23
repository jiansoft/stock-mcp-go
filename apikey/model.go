package apikey

import (
	"errors"
	"time"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusRevoked  Status = "revoked"
	// StatusExpired 是管理回應的衍生狀態，不會寫入 SQLite status 欄位。
	StatusExpired Status = "expired"
)

const HashAlgorithm = "hmac-sha256"

var (
	ErrNotFound   = errors.New("API key 不存在")
	ErrConflict   = errors.New("API key 已被其他操作更新")
	ErrLastActive = errors.New("不可停用或刪除最後一組啟用中的 API key")
	ErrClosed     = errors.New("API key service 已關閉")
	ErrValidation = errors.New("輸入驗證失敗")
)

// APIKey 是可供管理介面顯示的非敏感資料。完整密鑰與驗證雜湊不在此型別中。
type APIKey struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	MaskedKey   string     `json:"maskedKey"`
	Status      Status     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	Version     int64      `json:"version"`
}

type credential struct {
	ID        string
	Prefix    string
	Hash      []byte
	ExpiresAt *time.Time
}

type keyRecord struct {
	APIKey
	Prefix        string
	Hash          []byte
	HashAlgorithm string
}

// Principal 是通過驗證的呼叫端（會員系統計畫 6.3）。個人 Token 帶會員、家庭與 stock_go 解析完成的授權
// （grants：權限碼 → none／own／household／granted）；Legacy 為本機 SQLite 舊金鑰，Grants 為 nil 時沿用改版前的行為。
type Principal struct {
	KeyID       string
	Prefix      string
	MemberID    int64
	HouseholdID int64
	Household   []int64
	Account     string
	Grants      map[string]string
	Legacy      bool
}

type AuditEvent struct {
	KeyID     string
	KeyName   string
	KeyPrefix string
	Action    string
	Result    string
	CreatedAt time.Time
}
