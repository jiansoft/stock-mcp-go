// Package stock 測試即時選股的排除產業條件:驗證、query string 與 schema。
package stock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestScreenStocksExcludeIndustries 驗證排除產業的去重、上限、與 industry_id 衝突、實質篩選與 query string。
func TestScreenStocksExcludeIndustries(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"data_as_of":null,"stocks":[]}`))
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "secret", time.Second)
	if _, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "all", ExcludeIndustryIDs: []int{17, 15}, Limit: 20}); err != nil {
		t.Fatalf("ScreenStocks: %v", err)
	}
	if !strings.Contains(gotQuery, "exclude_industry_ids=17%2C15") {
		t.Fatalf("query 缺少 exclude_industry_ids:%s", gotQuery)
	}
	if _, err := client.ScreenStocks(t.Context(), ScreenOptions{Market: "twse", Limit: 20}); err != nil || strings.Contains(gotQuery, "exclude_industry_ids") {
		t.Fatalf("未設定時不應帶入:%s %v", gotQuery, err)
	}

	f := &fakeStockScreener{result: &Screening{}}
	if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, ScreenStocksInput{ExcludeIndustryIDs: []int{17, 17, 15}}); err != nil {
		t.Fatalf("排除產業本身就是篩選條件:%v", err)
	}
	if len(f.gotOpt.ExcludeIndustryIDs) != 2 || f.gotOpt.ExcludeIndustryIDs[0] != 17 || f.gotOpt.ExcludeIndustryIDs[1] != 15 {
		t.Fatalf("應去除重複並保留順序:%v", f.gotOpt.ExcludeIndustryIDs)
	}
	many := make([]int, 11)
	for i := range many {
		many[i] = i + 1
	}
	for _, in := range []ScreenStocksInput{
		{ExcludeIndustryIDs: []int{0}},
		{ExcludeIndustryIDs: many},
		{IndustryID: 17, ExcludeIndustryIDs: []int{17}},
	} {
		if _, _, err := newScreenToolset(f).screenStocks(t.Context(), nil, in); err == nil {
			t.Errorf("%#v 應回錯誤", in)
		}
	}
	property := screenStocksSchema().Properties["exclude_industry_ids"]
	if property == nil || property.Default != nil || property.Items == nil || *property.MaxItems != maxExcludedIndustries {
		t.Fatalf("schema 錯誤:%#v", property)
	}
}
