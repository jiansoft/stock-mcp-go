package stock

// 本檔案分成兩類測試:
//
//  1. SQL 稽核測試(TestSQLIsParameterized、
//     TestSQLQuotesCaseSensitiveIdentifiers):純粹用字串比對檢查
//     repository.go 裡的 SQL 常數字面內容,不需要連資料庫,跑得很快,
//     用來確保「絕不動態拼接 SQL」「大小寫敏感識別字都有加雙引號」這兩條
//     安全/正確性規則不會被日後的修改不小心破壞。
//  2. 整合測試(TestRepositoryIntegration):真的呼叫 Repository 的方法,
//     需要連上一個真實的 PostgreSQL 資料庫,因此由環境變數
//     TEST_DATABASE_URL 明確啟用,未設定時用 t.Skip 跳過——這是刻意的
//     設計,不能因為使用者的開發環境沒有資料庫連線,就讓 `go test ./...`
//     失敗或需要額外設定才能執行單元測試。

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSQLIsParameterized 稽核所有 SQL 常數都使用參數化查詢:
// 必須包含 $N 佔位符,且不得出現字串格式化動詞(%s、%d 等,
// 「%」只允許出現在 ILIKE 的萬用字元樣式裡——本專案的 SQL 常數
// 完全沒有 %,萬用字元是在 Go 端組進「綁定值」而非 SQL 字串)。
func TestSQLIsParameterized(t *testing.T) {
	queries := map[string]string{
		"searchStockSQL":      searchStockSQL,
		"latestDailyQuoteSQL": latestDailyQuoteSQL,
		"priceHistorySQL":     priceHistorySQL,
		"stockProfileSQL":     stockProfileSQL,
	}
	placeholder := regexp.MustCompile(`\$\d`)

	for name, sql := range queries {
		t.Run(name+" 使用 $N 佔位符且不含格式化動詞", func(t *testing.T) {
			if !placeholder.MatchString(sql) {
				t.Error("SQL 缺少參數化佔位符")
			}
			if strings.Contains(sql, "%") {
				t.Error("SQL 字串不可包含 %(值必須透過參數綁定傳入)")
			}
		})
	}
}

// TestSQLQuotesCaseSensitiveIdentifiers 確認大小寫敏感的欄位與表名
// 都正確加上雙引號,漏掉引號會讓 PostgreSQL 找不到欄位。
func TestSQLQuotesCaseSensitiveIdentifiers(t *testing.T) {
	for _, ident := range []string{`"SecurityCode"`, `"Name"`, `"SuspendListing"`} {
		if !strings.Contains(searchStockSQL, ident) {
			t.Errorf("searchStockSQL 缺少 %s", ident)
		}
	}
	for _, ident := range []string{`"DailyQuotes"`, `"Date"`, `"price-to-book_ratio"`} {
		if !strings.Contains(priceHistorySQL, ident) {
			t.Errorf("priceHistorySQL 缺少 %s", ident)
		}
	}
	for _, ident := range []string{`"maximum_price-to-book_ratio"`, `"minimum_price-to-book_ratio_date_on"`} {
		if !strings.Contains(stockProfileSQL, ident) {
			t.Errorf("stockProfileSQL 缺少 %s", ident)
		}
	}
}

// ---------------------------------------------------------------------------
// 整合測試:需要真實資料庫,由 TEST_DATABASE_URL 明確啟用,
// 未設定時跳過——不可要求使用者預設連線真實資料庫。
// ---------------------------------------------------------------------------

// newTestRepository 建立整合測試用的 Repository;
// 未設定 TEST_DATABASE_URL 時跳過測試。
func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("跳過整合測試:未設定 TEST_DATABASE_URL")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("建立測試連線池失敗:%v", err)
	}
	t.Cleanup(pool.Close)
	return NewRepository(pool)
}

func TestRepositoryIntegration(t *testing.T) {
	t.Run("SearchStock 查無資料回傳空 slice", func(t *testing.T) {
		repo := newTestRepository(t)
		stocks, err := repo.SearchStock(t.Context(), "ZZZZZ_NOT_EXIST_ZZZZZ", 10)
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if len(stocks) != 0 {
			t.Errorf("預期空結果,實際為 %d 筆", len(stocks))
		}
	})

	t.Run("LatestDailyQuote 未知代號回傳 nil", func(t *testing.T) {
		repo := newTestRepository(t)
		latest, err := repo.LatestDailyQuote(t.Context(), "ZZZZZ_NOT_EXIST_ZZZZZ")
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if latest != nil {
			t.Errorf("預期 nil,實際為 %+v", latest)
		}
	})

	t.Run("PriceHistory 未知代號回傳空 slice", func(t *testing.T) {
		repo := newTestRepository(t)
		var from, to *time.Time
		quotes, err := repo.PriceHistory(t.Context(), "ZZZZZ_NOT_EXIST_ZZZZZ", from, to, 30)
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if len(quotes) != 0 {
			t.Errorf("預期空結果,實際為 %d 筆", len(quotes))
		}
	})

	t.Run("StockProfile 未知代號回傳 nil", func(t *testing.T) {
		repo := newTestRepository(t)
		profile, err := repo.StockProfile(t.Context(), "ZZZZZ_NOT_EXIST_ZZZZZ")
		if err != nil {
			t.Fatalf("預期成功,實際錯誤:%v", err)
		}
		if profile != nil {
			t.Errorf("預期 nil,實際為 %+v", profile)
		}
	})
}
