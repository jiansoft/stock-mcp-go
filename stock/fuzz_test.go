package stock

// 本檔案對 tools.go 裡幾個「手寫的、直接處理 MCP 用戶端輸入」的解析與
// 驗證函式做模糊測試。
//
// 這幾個函式是本專案自己寫的(不是標準函式庫或 SDK 提供的),而且位於
// 信任邊界上:MCP 用戶端可以送出任意 JSON 字串進來,schema 只是第一層
// 提示,惡意或有問題的用戶端完全可以繞過 schema 直接發 JSON-RPC 請求。
// fuzz 的價值在於自動探索那些人工想不到的輸入組合(超長字串、非 UTF-8
// 位元組、Unicode 邊界、只有空白的字串等)。

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func FuzzNormalizeSymbol(f *testing.F) {
	for _, s := range []string{"", "2330", " 2330 ", "00631l", "台積電", "\x00", strings.Repeat("a", 100), "２３３０"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		symbol, err := normalizeSymbol(raw)
		if err != nil {
			// 驗證失敗時必須回傳空字串,不可以「既報錯又回傳一個值」——
			// 呼叫端若不小心忽略了 err,也不該拿到一個半處理過的代號。
			if symbol != "" {
				t.Fatalf("驗證失敗時不應回傳值:raw=%q symbol=%q err=%v", raw, symbol, err)
			}
			return
		}

		// 性質一:通過驗證的代號長度必須落在宣告的 1–24 字元(以 Unicode
		// 字元數計,不是位元組數)範圍內。這是防止「用中文字繞過長度上限」
		// 的關鍵——若這裡改用 len(),24 個中文字會被算成 72 而誤擋。
		if n := utf8.RuneCountInString(symbol); n < 1 || n > 24 {
			t.Fatalf("通過驗證的代號長度應在 1–24 之間:raw=%q symbol=%q n=%d", raw, symbol, n)
		}

		// 性質二:結果必須已經去除前後空白。若沒去乾淨,"2330 " 與 "2330"
		// 會變成兩個不同的查詢鍵。
		if symbol != strings.TrimSpace(symbol) {
			t.Fatalf("結果應已去除前後空白:raw=%q symbol=%q", raw, symbol)
		}

		// 性質三:冪等性——把結果再正規化一次,必須得到完全相同的值。
		// 這是「同一支股票永遠對應同一個查詢鍵」的基礎;若不冪等,快取或
		// 錯誤訊息就可能出現同一支股票的兩種寫法。
		again, err2 := normalizeSymbol(symbol)
		if err2 != nil || again != symbol {
			t.Fatalf("正規化應具冪等性:symbol=%q again=%q err=%v", symbol, again, err2)
		}
	})
}

func FuzzParseDateArg(f *testing.F) {
	for _, s := range []string{"", "2026-07-26", "2026-13-40", "2026-02-30", "0000-00-00", "9999-99-99", "2026-7-6", "２０２６-０７-２６"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := parseDateArg(raw, "from")
		if err != nil {
			if parsed != nil {
				t.Fatalf("驗證失敗時不應回傳日期:raw=%q err=%v", raw, err)
			}
			return
		}

		// 空字串代表「未提供」,依契約回傳 (nil, nil)。
		if raw == "" {
			if parsed != nil {
				t.Fatalf("空字串應回傳 nil 日期,實際為 %v", *parsed)
			}
			return
		}

		if parsed == nil {
			t.Fatalf("非空輸入通過驗證時應回傳日期:raw=%q", raw)
		}

		// 性質:通過驗證的日期,格式化回字串必須與原輸入完全相同。
		//
		// 這一條同時擋掉兩類 bug:time.Parse 對某些輸入會「寬容地正規化」
		// (例如把不存在的日期捲到下個月),以及正則通過但語意不同的
		// 輸入。若 round-trip 不相等,代表我們接受了一個「其實不是使用者
		// 所指」的日期,而它接著會被送去查詢資料庫。
		if got := parsed.Format("2006-01-02"); got != raw {
			t.Fatalf("日期 round-trip 不一致:raw=%q parsed=%q", raw, got)
		}
	})
}

func FuzzParseMonthArg(f *testing.F) {
	for _, s := range []string{"", "2026-07", "2026-13", "2026-00", "26-07", "2026-7", "2026-07-01"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		month, err := parseMonthArg(raw, "from")
		if err != nil {
			if month != "" {
				t.Fatalf("驗證失敗時不應回傳值:raw=%q month=%q", raw, month)
			}
			return
		}

		// 通過驗證時原樣回傳(伺服器端吃同一種格式,不需要轉換)。
		if month != raw {
			t.Fatalf("通過驗證時應原樣回傳:raw=%q month=%q", raw, month)
		}
		if raw == "" {
			return
		}

		// 性質:通過驗證的月份必須恰好是 YYYY-MM,且月份介於 01–12。
		// 月份範圍很重要——它會被直接放進送往 Data API 的 query string。
		if len(month) != 7 || month[4] != '-' {
			t.Fatalf("格式應為 YYYY-MM:%q", month)
		}
		mm := month[5:]
		if mm < "01" || mm > "12" {
			t.Fatalf("月份應介於 01–12:%q", month)
		}
	})
}

func FuzzRangedLimit(f *testing.F) {
	for _, v := range []int{0, 1, 30, 365, -1, 1 << 40} {
		f.Add(v)
	}

	f.Fuzz(func(t *testing.T, value int) {
		const (
			def = 30
			min = 1
			max = 365
		)
		limit, err := rangedLimit(value, def, min, max)
		if err != nil {
			if limit != 0 {
				t.Fatalf("驗證失敗時不應回傳值:value=%d limit=%d", value, limit)
			}
			return
		}

		// 性質:任何通過驗證的 limit 都必須落在合法範圍內。這個值會直接
		// 變成 SQL 的 LIMIT 或 API 的 limit 參數,一個負數或極大值會造成
		// 查詢錯誤或把整張表撈出來。
		if limit < min || limit > max {
			t.Fatalf("回傳的 limit 超出範圍:value=%d limit=%d", value, limit)
		}
		// 0 代表「未提供」,必須套用預設值。
		if value == 0 && limit != def {
			t.Fatalf("未提供時應套用預設值 %d,實際為 %d", def, limit)
		}
	})
}

// TestValidateDividendYears 驗證年度區間的邊界。
//
// 因為 validateDividendYears 現在是純函式(maxYear 由呼叫端傳入),這裡
// 可以直接釘住「剛好等於上限」與「超過上限一年」這兩個關鍵案例;若上限
// 仍寫死成 time.Now().Year()+1,這種測試就得跟著真實時間跑,跨年時行為
// 會改變。
func TestValidateDividendYears(t *testing.T) {
	const maxYear = 2027

	tests := []struct {
		name             string
		fromYear, toYear int
		wantErr          bool
	}{
		{"都未提供(0 值)不檢查", 0, 0, false},
		{"合法區間", 2020, 2026, false},
		{"下限邊界 1990", 1990, 1990, false},
		{"上限邊界剛好等於 maxYear", maxYear, maxYear, false},
		{"低於下限一年", 1989, 2026, true},
		{"超過上限一年", 2020, maxYear + 1, true},
		{"只提供 from 且超出範圍", maxYear + 1, 0, true},
		{"from 晚於 to", 2026, 2020, true},
		{"from 等於 to 合法", 2026, 2026, false},
		{"只提供其中一個不比較先後", 2026, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDividendYears(tt.fromYear, tt.toYear, maxYear)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateDividendYears(%d, %d, %d) error = %v,預期出錯 = %v",
					tt.fromYear, tt.toYear, maxYear, err, tt.wantErr)
			}
		})
	}

	t.Run("maxDividendYear 是今年加一", func(t *testing.T) {
		if got, want := maxDividendYear(), time.Now().Year()+1; got != want {
			t.Errorf("maxDividendYear() = %d,預期 %d", got, want)
		}
	})
}

// TestEscapeLike 驗證 ILIKE 樣式的萬用字元轉義。
func TestEscapeLike(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"一般文字不變", "台積電", "台積電"},
		{"百分號被轉義", "%", `\%`},
		{"底線被轉義", "_", `\_`},
		{"反斜線被轉義", `\`, `\\`},
		{"混合", `a%b_c\d`, `a\%b\_c\\d`},
		{"空字串", "", ""},
		{"已經轉義過的內容會被再次轉義(不做猜測)", `\%`, `\\\%`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeLike(tt.in); got != tt.want {
				t.Errorf("escapeLike(%q) = %q,預期 %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("單一 % 不再變成比對全部的樣式", func(t *testing.T) {
		// 這是這個轉義真正要防的行為:未轉義時樣式會是 "%%%",意思是
		// 「比對所有資料」,一次搜尋就變成全表掃描。
		pattern := "%" + escapeLike("%") + "%"
		if pattern == "%%%" {
			t.Fatal("單一 %% 仍會產生比對全部的樣式")
		}
		if pattern != `%\%%` {
			t.Errorf("樣式不正確:%q", pattern)
		}
	})
}
