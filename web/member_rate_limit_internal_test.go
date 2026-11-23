package web

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestMemberRequestLimiterSweepsAndCaps：閒置到額度補滿的會員桶每分鐘清掉一次；桶數達上限時新會員先等 60 秒。
func TestMemberRequestLimiterSweepsAndCaps(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	limiter := memberRequestLimiter{perSec: rate.Limit(1), burst: 2, buckets: make(map[int64]memberBucket), sweptAt: now}
	if wait := limiter.retryAfter(1, now); wait != 0 {
		t.Fatalf("第一個請求應放行：%d", wait)
	}
	now = now.Add(2 * time.Minute)
	limiter.retryAfter(2, now)
	if _, ok := limiter.buckets[1]; ok {
		t.Fatal("閒置超過補滿時間的會員桶應被清掉")
	}

	full := memberRequestLimiter{perSec: rate.Limit(1), burst: 2, buckets: make(map[int64]memberBucket, defaultMaxBuckets), sweptAt: now}
	for id := range int64(defaultMaxBuckets) {
		full.buckets[id+100] = memberBucket{limiter: rate.NewLimiter(1, 2), last: now}
	}
	if wait := full.retryAfter(1, now); wait != 60 {
		t.Fatalf("桶數已滿時新會員應等 60 秒：%d", wait)
	}
}
