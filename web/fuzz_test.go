package web

// FuzzForwardedFor 對 X-Forwarded-For 的解析做模糊測試。
//
// ## 為什麼是這個函式?
//
// forwardedFor 同時滿足「值得 fuzz」的三個條件:
//   - 它處理的是完全不可信的輸入(HTTP header,任何人都能任意填寫)。
//   - 它是手寫的字串掃描邏輯(索引運算、切片),這正是最容易寫出越界
//     存取的地方。
//   - 它是安全機制的一部分——回傳值決定 rate limit 的分桶,一旦它回傳
//     了攻擊者可控的值,限流就形同虛設。
//
// fuzz 執行時會自動嘗試大量畸形輸入(空字串、只有逗號、超長、非 UTF-8、
// 各種空白組合),檢查下面這些性質是否恆真。

import (
	"net"
	"strings"
	"testing"
)

func FuzzForwardedFor(f *testing.F) {
	// seed corpus:涵蓋正常格式與各種邊界,fuzz 引擎會以它們為起點變異。
	seeds := []struct {
		xff  string
		hops int
	}{
		{"", 1},
		{"198.51.100.99", 1},
		{"1.2.3.4, 198.51.100.99", 1},
		{"1.2.3.4, 198.51.100.99, 10.0.0.2", 2},
		{",", 1},
		{",,,", 3},
		{"   ", 1},
		{"2001:db8::1", 1},
		{"not-an-ip, 198.51.100.99", 1},
		{strings.Repeat("1.1.1.1,", 100) + "2.2.2.2", 1},
	}
	for _, s := range seeds {
		f.Add(s.xff, s.hops)
	}

	f.Fuzz(func(t *testing.T, xff string, hops int) {
		// hops 在正式流程中由 config 驗證過(1–10),這裡把 fuzz 產生的
		// 任意整數收斂到同一個範圍,才不會浪費算力在「呼叫端保證不會發生」
		// 的輸入上。
		if hops < 1 || hops > 10 {
			return
		}

		got := forwardedFor(xff, hops)

		// 性質一:回傳值只可能是空字串,或一個合法的 IP 位址。絕不可以
		// 回傳「看起來像但其實不是 IP」的東西,因為那會變成 rate limit 的
		// map key,也可能被寫進 log。
		if got != "" && net.ParseIP(got) == nil {
			t.Fatalf("回傳了非法 IP:xff=%q hops=%d got=%q", xff, hops, got)
		}

		// 性質二:回傳值必須真的出現在輸入裡(去除空白後)。這排除了
		// 「因為索引算錯而拼出一段原本不存在的字串」這類 bug。
		if got != "" && !strings.Contains(xff, got) {
			t.Fatalf("回傳值不存在於輸入中:xff=%q hops=%d got=%q", xff, hops, got)
		}

		// 性質三:同樣的輸入必須永遠得到同樣的輸出。rate limit 依賴這個
		// 穩定性,否則同一個用戶端會在不同請求間被分到不同的桶。
		if again := forwardedFor(xff, hops); again != got {
			t.Fatalf("結果不穩定:xff=%q hops=%d 第一次=%q 第二次=%q", xff, hops, got, again)
		}
	})
}
