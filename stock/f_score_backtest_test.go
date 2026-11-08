package stock

import (
	"strings"
	"testing"
)

// TestBacktestFScoreFilter 驗證回測的 F 分數下限:query、範圍檢查、schema 與摘要文字。
func TestBacktestFScoreFilter(t *testing.T) {
	opt, err := screenBacktestOptions(ScreenBacktestInput{MinFScore: ptr(7)})
	if err != nil {
		t.Fatalf("不應失敗:%v", err)
	}
	if got := opt.query().Get("min_f_score"); got != "7" {
		t.Fatalf("query min_f_score = %q,應為 7", got)
	}
	plain, _ := screenBacktestOptions(ScreenBacktestInput{})
	if plain.query().Has("min_f_score") {
		t.Fatal("未設定時不應帶入 query")
	}
	for _, bad := range []ScreenBacktestInput{{MinFScore: ptr(-1)}, {MinFScore: ptr(10)}} {
		if _, err := screenBacktestOptions(bad); err == nil {
			t.Errorf("%+v 應回錯誤", bad)
		}
	}
	if backtestFundamentalSchemas()["min_f_score"] == nil {
		t.Fatal("回測 schema 缺少 min_f_score")
	}
	want := "已公布年度 F 分數 ≥ 7"
	if text := backtestOptionsText(opt); !strings.Contains(text, want) {
		t.Fatalf("穩健度摘要缺少 F 分數條件:%s", text)
	}
	e := sampleScreenBacktest()
	e.MinFScore = ptr(7)
	if text := screenBacktestSummary(e); !strings.Contains(text, want) {
		t.Fatalf("回測摘要缺少 F 分數條件:%s", text)
	}
	if fScoreTexts(nil) != nil {
		t.Fatal("沒有條件應為 nil")
	}
}
