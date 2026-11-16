package stock

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
)

// TestResolveMemberID：本人範圍未傳帶入本人、傳 0 或他人拒絕；家庭範圍限同家庭；舊金鑰沿用傳入值。
func TestResolveMemberID(t *testing.T) {
	own := apikey.Principal{MemberID: 2, Household: []int64{1, 2}, Grants: map[string]string{PermPortfolioView: "own"}}
	household := apikey.Principal{MemberID: 2, Household: []int64{1, 2}, Grants: map[string]string{PermPortfolioView: "household"}}
	cases := []struct {
		name      string
		principal apikey.Principal
		requested int64
		present   bool
		want      int64
		wantErr   bool
	}{
		{"本人未傳", own, 0, false, 2, false},
		{"本人傳自己", own, 2, true, 2, false},
		{"本人傳 0", own, 0, true, 0, true},
		{"本人傳他人", own, 1, true, 0, true},
		{"家庭合計", household, 0, false, 0, false},
		{"家庭成員", household, 1, true, 1, false},
		{"其他家庭", household, 9, true, 0, true},
		{"舊金鑰", apikey.Principal{Legacy: true}, 7, true, 7, false},
		{"沒有權限", apikey.Principal{Grants: map[string]string{}}, 0, false, 0, true},
	}
	for _, tc := range cases {
		got, err := resolveMemberID(tc.principal, PermPortfolioView, tc.requested, tc.present)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s：得到 %d, %v", tc.name, got, err)
		}
	}
	if _, err := resolveMemberID(own, PermPortfolioView, 1, true); !errors.Is(err, errOwnOnly) {
		t.Fatalf("本人範圍的錯誤文字應固定：%v", err)
	}
}

// TestArgumentPresentAndMemberField：區分「未傳」與「傳 0」；null 視為未傳；欄位以 json 名稱尋找。
func TestArgumentPresentAndMemberField(t *testing.T) {
	req := func(raw string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(raw)}}
	}
	for raw, want := range map[string]bool{`{"member_id":0}`: true, `{}`: false, `{"member_id":null}`: false, `not-json`: false} {
		if got := argumentPresent(req(raw), "member_id"); got != want {
			t.Errorf("%s：得到 %v", raw, got)
		}
	}
	if argumentPresent(nil, "member_id") {
		t.Error("沒有請求應視為未傳")
	}
	if _, ok := memberIDField(reflect.TypeFor[SetTargetsInput]()); !ok {
		t.Error("SetTargetsInput 應找到 member_id")
	}
	if _, ok := memberIDField(reflect.TypeFor[SymbolInput]()); ok {
		t.Error("SymbolInput 不應有 member_id")
	}
	if _, ok := memberIDField(reflect.TypeFor[string]()); ok {
		t.Error("非 struct 不應有 member_id")
	}
}

// TestScopedPathAndBody：只有持股類端點且 context 有資料範圍時才補 household_id 與 actor_*。
func TestScopedPathAndBody(t *testing.T) {
	ctx := withDataScope(context.Background(), dataScope{HouseholdID: 3, ActorRef: "tok"})
	if got := scopedPath(ctx, "/api/v1/portfolio/summary?member_id=1"); got != "/api/v1/portfolio/summary?member_id=1&household_id=3" {
		t.Errorf("持股 GET：%s", got)
	}
	if got := scopedPath(ctx, "/api/v1/portfolio/chip"); got != "/api/v1/portfolio/chip?household_id=3" {
		t.Errorf("沒有參數的持股 GET：%s", got)
	}
	if got := scopedPath(ctx, "/api/v1/stocks/2330"); got != "/api/v1/stocks/2330" {
		t.Errorf("市場端點不應改動：%s", got)
	}
	if got := scopedPath(context.Background(), "/api/v1/portfolio/summary"); got != "/api/v1/portfolio/summary" {
		t.Errorf("沒有資料範圍不應改動：%s", got)
	}
	body, err := scopedBody(ctx, "/api/v1/portfolio/targets", []byte(`{"member_id":2}`))
	var fields map[string]any
	if err != nil || json.Unmarshal(body, &fields) != nil || fields["household_id"] != float64(3) || fields["actor_source"] != "mcp" || fields["actor_ref"] != "tok" || fields["member_id"] != float64(2) {
		t.Fatalf("寫入 body：%s %v", body, err)
	}
	if body, _ := scopedBody(ctx, "/api/v1/portfolio/targets", []byte(`null`)); string(body) == "null" {
		t.Error("null body 也應補上欄位")
	}
	if _, err := scopedBody(ctx, "/api/v1/portfolio/targets", []byte(`[1]`)); err == nil {
		t.Error("非物件 body 應回錯誤")
	}
	if body, _ := scopedBody(context.Background(), "/api/v1/portfolio/targets", []byte(`{}`)); string(body) != `{}` {
		t.Errorf("沒有資料範圍不應改動：%s", body)
	}
}

// TestRegisterPersonalToolRequiresMemberField：Personal 工具的輸入缺 member_id 時無法註冊。
func TestRegisterPersonalToolRequiresMemberField(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("缺 member_id 的 Personal 工具應 panic")
		}
	}()
	server := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "test"}, nil)
	registerTool(server, &mcp.Tool{Name: "get_portfolio_summary"}, func(context.Context, *mcp.CallToolRequest, SymbolInput) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
}
