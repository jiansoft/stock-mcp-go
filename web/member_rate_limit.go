package web

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type memberBucket struct {
	limiter *rate.Limiter
	last    time.Time
}

type memberRequestLimiter struct {
	mu      sync.Mutex
	perSec  rate.Limit
	burst   int
	buckets map[int64]memberBucket
	sweptAt time.Time
}

func (l *memberRequestLimiter) retryAfter(memberID int64, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.sweptAt) >= time.Minute {
		refill := float64(l.burst) / float64(l.perSec)
		for id, bucket := range l.buckets {
			if now.Sub(bucket.last).Seconds() >= refill {
				delete(l.buckets, id)
			}
		}
		l.sweptAt = now
	}
	bucket, ok := l.buckets[memberID]
	if !ok {
		if len(l.buckets) >= defaultMaxBuckets {
			return 60
		}
		bucket.limiter = rate.NewLimiter(l.perSec, l.burst)
	}
	bucket.last = now
	l.buckets[memberID] = bucket
	if bucket.limiter.AllowN(now, 1) {
		return 0
	}
	wait := (1 - bucket.limiter.TokensAt(now)) / float64(l.perSec)
	return max(1, int(math.Ceil(wait)))
}

// rateLimitMemberRequests 依已驗證的 MemberID 計數，同一會員的所有來源與 Token 共用額度。
func rateLimitMemberRequests(perMinute, burst int, next http.Handler) http.Handler {
	limiter := memberRequestLimiter{perSec: rate.Limit(float64(perMinute) / 60), burst: burst, buckets: make(map[int64]memberBucket)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authenticatedPrincipal(r)
		if !ok || principal.MemberID <= 0 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"未經授權"}`))
			return
		}
		if wait := limiter.retryAfter(principal.MemberID, time.Now()); wait > 0 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
