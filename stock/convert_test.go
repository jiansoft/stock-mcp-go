package stock

// 本檔案測試 convert.go 的三個轉換函式,核心是驗證「NULL 就是 NULL,
// 絕不偽裝成 0」這條規則在各種邊界情況(NaN、無限大、真的是 NULL)下
// 都確實回傳 nil,以及正常數值/日期/時間能被正確轉換成對外格式。

import (
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestNumericToFloat(t *testing.T) {
	tests := []struct {
		name string
		in   pgtype.Numeric
		want *float64
	}{
		{
			// 12345 × 10^-2 = 123.45,對應資料庫 NUMERIC 的內部表示法。
			name: "一般數值正確轉為 float64",
			in:   pgtype.Numeric{Int: big.NewInt(12345), Exp: -2, Valid: true},
			want: ptr(123.45),
		},
		{
			name: "整數數值",
			in:   pgtype.Numeric{Int: big.NewInt(2330), Exp: 0, Valid: true},
			want: ptr(2330.0),
		},
		{
			name: "負數數值",
			in:   pgtype.Numeric{Int: big.NewInt(-15), Exp: -1, Valid: true},
			want: ptr(-1.5),
		},
		{
			// 規格核心要求:NULL 必須維持 null,不可轉為 0。
			name: "NULL 維持 nil",
			in:   pgtype.Numeric{Valid: false},
			want: nil,
		},
		{
			name: "NaN 轉為 nil 以避免不合法的 JSON",
			in:   pgtype.Numeric{NaN: true, Valid: true},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := numericToFloat(tt.in)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("預期 nil,實際為 %v", *got)
			case tt.want != nil && got == nil:
				t.Errorf("預期 %v,實際為 nil", *tt.want)
			case tt.want != nil && got != nil && *got != *tt.want:
				t.Errorf("預期 %v,實際為 %v", *tt.want, *got)
			}
		})
	}
}

func TestDateToString(t *testing.T) {
	t.Run("日期輸出 YYYY-MM-DD", func(t *testing.T) {
		d := pgtype.Date{Time: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC), Valid: true}
		got := dateToString(d)
		if got == nil || *got != "2026-07-12" {
			t.Fatalf("預期 2026-07-12,實際為 %v", got)
		}
	})

	t.Run("NULL 日期維持 nil", func(t *testing.T) {
		if got := dateToString(pgtype.Date{Valid: false}); got != nil {
			t.Fatalf("預期 nil,實際為 %v", *got)
		}
	})

	t.Run("infinity 日期視為 nil", func(t *testing.T) {
		d := pgtype.Date{InfinityModifier: pgtype.Infinity, Valid: true}
		if got := dateToString(d); got != nil {
			t.Fatalf("預期 nil,實際為 %v", *got)
		}
	})
}

func TestTimestampToString(t *testing.T) {
	t.Run("timestamp 輸出 UTC ISO 8601", func(t *testing.T) {
		loc := time.FixedZone("Asia/Taipei", 8*3600)
		ts := pgtype.Timestamptz{
			Time:  time.Date(2026, 7, 12, 21, 30, 0, 0, loc),
			Valid: true,
		}
		got := timestampToString(ts)
		if got == nil || *got != "2026-07-12T13:30:00Z" {
			t.Fatalf("預期 2026-07-12T13:30:00Z,實際為 %v", got)
		}
	})

	t.Run("NULL timestamp 維持 nil", func(t *testing.T) {
		if got := timestampToString(pgtype.Timestamptz{Valid: false}); got != nil {
			t.Fatalf("預期 nil,實際為 %v", *got)
		}
	})
}
