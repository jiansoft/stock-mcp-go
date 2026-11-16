package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
)

// 個人資料工具的會員鎖定（會員系統計畫 6.3）：Personal 工具的 member_id 一律經 resolveMemberID，
// 呼叫 Data API 時帶 Token 所屬的 household_id（5.0.1），寫入帶 actor_source=mcp 與 Token id。

// legacyHouseholdID 是舊金鑰尚未套用會員權限時使用的家庭（會員系統上線前只有家庭 #1）。
const legacyHouseholdID int64 = 1

// errOwnOnly 是本人範圍的 Token 指定 0 或他人時的錯誤（規格 6.3 的固定文字）。
var errOwnOnly = errors.New("此 Token 僅限查詢本人資料")

// dataScope 是個人資料呼叫 Data API 時要帶的家庭與寫入來源；APIClient 依此補上 household_id 與 actor_*。
type dataScope struct {
	HouseholdID int64
	ActorRef    string
}

type dataScopeContextKey struct{}

func withDataScope(ctx context.Context, scope dataScope) context.Context {
	return context.WithValue(ctx, dataScopeContextKey{}, scope)
}

func dataScopeFrom(ctx context.Context) (dataScope, bool) {
	scope, ok := ctx.Value(dataScopeContextKey{}).(dataScope)
	return scope, ok
}

// resolveMemberID 依 Token 對該權限的範圍決定實際查詢的會員：
//   - 本人範圍：未傳時帶入本人；明確傳 0 或他人回 errOwnOnly。
//   - 家庭範圍：未傳或 0 為該家庭合計，或同家庭任一成員；其他家庭的成員回權限錯誤。
//   - 舊金鑰尚未套用會員權限（Legacy 且沒有 grants）：沿用傳入值。
func resolveMemberID(principal apikey.Principal, permission string, requested int64, present bool) (int64, error) {
	if principal.Legacy && principal.Grants == nil {
		return requested, nil
	}
	switch principal.Grants[permission] {
	case "own":
		if !present {
			return principal.MemberID, nil
		}
		if requested != principal.MemberID {
			return 0, errOwnOnly
		}
		return requested, nil
	case "household":
		if requested == 0 || requested == principal.MemberID || slices.Contains(principal.Household, requested) {
			return requested, nil
		}
		return 0, fmt.Errorf("會員 %d 不屬於此 Token 的家庭", requested)
	default:
		return 0, fmt.Errorf("此 Token 沒有 %s 權限", permission)
	}
}

// memberIDField 回傳輸入型別中 json 名稱為 member_id 的 int64 欄位位置；Personal 工具缺少時註冊即 panic，
// 確保每個 Personal 工具的會員都經過 resolveMemberID。
func memberIDField(t reflect.Type) (int, bool) {
	if t.Kind() != reflect.Struct {
		return 0, false
	}
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "member_id" && field.Type.Kind() == reflect.Int64 {
			return i, true
		}
	}
	return 0, false
}

// argumentPresent 判斷呼叫端是否明確傳入某個參數（null 視為未傳）；SDK 套用 schema 預設值不影響原始參數。
func argumentPresent(req *mcp.CallToolRequest, name string) bool {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return false
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return false
	}
	raw, ok := args[name]
	return ok && string(raw) != "null"
}

// scopePersonal 鎖定 Personal 工具的 member_id 並把家庭與寫入來源放進 context；沒有呼叫端身分（測試）時不變。
func scopePersonal[In any](ctx context.Context, req *mcp.CallToolRequest, in *In, field int, permission string, principal apikey.Principal, found bool) (context.Context, error) {
	if !found {
		return ctx, nil
	}
	value := reflect.ValueOf(in).Elem().Field(field)
	resolved, err := resolveMemberID(principal, permission, value.Int(), argumentPresent(req, "member_id"))
	if err != nil {
		return ctx, err
	}
	value.SetInt(resolved)
	household := principal.HouseholdID
	if household <= 0 {
		household = legacyHouseholdID
	}
	return withDataScope(ctx, dataScope{HouseholdID: household, ActorRef: principal.KeyID}), nil
}
