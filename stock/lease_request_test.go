package stock

import (
	"strings"
	"testing"
	"time"
)

// TestStockGoLeasesInvalidBaseURL：stock_go 位址無法組成請求時回錯誤，不送出租約請求。
func TestStockGoLeasesInvalidBaseURL(t *testing.T) {
	leases, err := NewStockGoLeases("http://bad\x7fhost", "quota-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.IssueLease(t.Context(), 2, "/api/v1/stocks/screen/backtest"); err == nil || !strings.Contains(err.Error(), "建立租約請求") {
		t.Fatalf("無法建立請求應回錯誤：%v", err)
	}
}
