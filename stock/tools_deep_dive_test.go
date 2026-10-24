package stock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeepDivePartialFailureAndConcurrency 驗證聚合一次取得資料、並行上限及錯誤不洩漏。
func TestDeepDivePartialFailureAndConcurrency(t *testing.T) {
	var active, peak, calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("缺少驗證")
		}
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		if strings.Contains(r.URL.Path, "profile") {
			_, _ = w.Write([]byte(`{"stock_symbol":"2330"}`))
			return
		}
		time.Sleep(10 * time.Millisecond)
		if strings.HasSuffix(r.URL.Path, "chip") {
			http.Error(w, "SQL internal secret", 500)
			return
		}
		_, _ = w.Write([]byte(`{"data_as_of":"2026-10-23","items":[]}`))
	}))
	defer upstream.Close()
	result, err := NewAPIClient(upstream.URL, "secret", time.Second).AnalyzeStockDeepDive(t.Context(), "2330")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 10 || len(result.Sections) != 9 || len(result.Warnings) != 1 {
		t.Fatalf("聚合結果不符:%+v calls=%d", result, calls.Load())
	}
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("並行數不符:%d", peak.Load())
	}
	if strings.Contains(result.Warnings["chip"], "secret") {
		t.Fatal("洩漏內部錯誤")
	}
}

func TestDeepDiveMissingStockStopsBeforeFanout(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) }))
	defer upstream.Close()
	if _, err := NewAPIClient(upstream.URL, "", time.Second).AnalyzeStockDeepDive(t.Context(), "9999"); err == nil || calls.Load() != 1 {
		t.Fatalf("查無應立即停止: %v %d", err, calls.Load())
	}
}

// TestDeepDiveSectionErrorsAreClassifiedAndRecorded 驗證 404 與暫時失敗的警示不同，實際錯誤保留供記錄 log。
func TestDeepDiveSectionErrorsAreClassifiedAndRecorded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "profile"):
			_, _ = w.Write([]byte(`{"stock_symbol":"2330"}`))
		case strings.HasSuffix(r.URL.Path, "chip"):
			w.WriteHeader(404)
		case strings.HasSuffix(r.URL.Path, "dividends"):
			http.Error(w, "SQL internal secret", 422)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer upstream.Close()
	result, err := NewAPIClient(upstream.URL, "", time.Second).AnalyzeStockDeepDive(t.Context(), "2330")
	if err != nil {
		t.Fatal(err)
	}
	if result.Warnings["chip"] != "此區塊查無資料" || result.Warnings["dividends"] != "此區塊目前無法取得" {
		t.Fatalf("警示分類不符:%v", result.Warnings)
	}
	if result.failures["dividends"] == nil || result.failures["chip"] != nil {
		t.Fatalf("只有真正的錯誤才應記錄:%v", result.failures)
	}
	if strings.Contains(result.Warnings["dividends"], "secret") {
		t.Fatal("洩漏內部錯誤")
	}
}

// TestDeepDiveTimeoutWithoutAnySectionIsAnError 驗證整體逾時且沒有任何區塊成功時回傳錯誤，而非空結果。
func TestDeepDiveTimeoutWithoutAnySectionIsAnError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "profile") {
			_, _ = w.Write([]byte(`{"stock_symbol":"2330"}`))
			return
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	result, err := NewAPIClient(upstream.URL, "", 5*time.Second).AnalyzeStockDeepDive(ctx, "2330")
	if !errors.Is(err, context.DeadlineExceeded) || result != nil {
		t.Fatalf("應回傳逾時錯誤: %v %+v", err, result)
	}
}
