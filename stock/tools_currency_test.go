package stock

import (
	"math"
	"testing"
)

// TestCurrencyUnits 驗證不同來源單位、負值、缺值與小額不歸零的顯示。
func TestCurrencyUnits(t *testing.T) {
	cases := []struct {
		value float64
		unit  CurrencyUnit
		want  string
	}{
		{999, CurrencyYuan, "999 元"}, {-999, CurrencyYuan, "-999 元"},
		{10, CurrencyThousand, "1 萬"}, {100, CurrencyMillion, "1 億"},
		{-123450000, CurrencyYuan, "-1.2 億"}, {math.NaN(), CurrencyYuan, "無"},
		{math.Inf(1), CurrencyYuan, "無"}, {1, CurrencyUnit(0), "無"},
		{-0.1, CurrencyYuan, "0 元"},
	}
	for _, tc := range cases {
		if got := FormatCurrencyWithUnit(&tc.value, tc.unit); got != tc.want {
			t.Errorf("%v %v = %q; want %q", tc.value, tc.unit, got, tc.want)
		}
	}
	if got := FormatCurrencyWithUnit(nil, CurrencyYuan); got != "無" {
		t.Fatal(got)
	}
}
