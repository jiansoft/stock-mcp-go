package stock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
)

// listAllTools 以完整實作所有查詢介面的 APIClient 註冊全部工具（註冊時不連線），回傳工具清單。
func listAllTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "policy-test", Version: "test"}, nil)
	AddTools(server, NewAPIClient("http://127.0.0.1:1", "k", time.Second), nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "policy-client", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	var tools []*mcp.Tool
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tool)
	}
	return tools
}

// TestEveryToolDeclaresPolicy：全部工具都在政策表內、政策表沒有已不存在的工具；
// 帶 member_id 參數的工具一律標示 Personal（6.3）。
func TestEveryToolDeclaresPolicy(t *testing.T) {
	tools := listAllTools(t)
	if len(tools) != len(toolPolicies) {
		t.Errorf("註冊 %d 個工具，政策表有 %d 項：兩者應一致", len(tools), len(toolPolicies))
	}
	personal := 0
	for _, tool := range tools {
		policy, ok := PolicyOf(tool.Name)
		if !ok {
			t.Errorf("%s 沒有政策", tool.Name)
			continue
		}
		if err := authorizeTool(tool.Name, policy, apikey.Principal{}, true); err == nil {
			t.Errorf("%s 不可接受缺少權限的會員 Token", tool.Name)
		}
		scope := "granted"
		if policy.Personal {
			scope = "household"
		}
		p := apikey.Principal{Grants: map[string]string{policy.Permission: scope}}
		if err := authorizeTool(tool.Name, policy, p, true); err != nil {
			t.Errorf("%s 應接受具備工具政策權限的會員 Token：%v", tool.Name, err)
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(schema), `"member_id"`) && !policy.Personal {
			t.Errorf("%s 帶 member_id 參數，應標示 Personal", tool.Name)
		}
		if policy.Personal {
			personal++
		}
	}
	if personal != 15 {
		t.Errorf("持股類 Personal 工具應為 15 個，實際 %d", personal)
	}
}

// TestRegisterToolRequiresPolicy：未宣告政策的工具無法註冊。
func TestRegisterToolRequiresPolicy(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未宣告政策的工具應 panic")
		}
	}()
	server := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "test"}, nil)
	registerTool(server, &mcp.Tool{Name: "brand_new_tool"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	})
}

// TestAuthorizeTool：grants 為 none 或缺少即拒絕；舊金鑰尚未套用會員權限時沿用改版前行為；
// 正式環境沒有呼叫端身分一律拒絕。
func TestAuthorizeTool(t *testing.T) {
	token := apikey.Principal{KeyID: "t", Grants: map[string]string{PermMarketView: "granted", PermPortfolioView: "own", PermPortfolioEdit: "none"}}
	cases := []struct {
		name      string
		policy    ToolPolicy
		principal apikey.Principal
		found     bool
		ok        bool
	}{
		{"市場權限", market(), token, true, true},
		{"持股本人", personal(PermPortfolioView), token, true, true},
		{"寫入 none", personal(PermPortfolioEdit), token, true, false},
		{"沒有回測", analysis(), token, true, false},
		{"舊金鑰未套權限", analysis(), apikey.Principal{Legacy: true}, true, true},
		{"沒有身分（測試）", market(), apikey.Principal{}, false, true},
	}
	for _, tc := range cases {
		if err := authorizeTool("x", tc.policy, tc.principal, tc.found); (err == nil) != tc.ok {
			t.Errorf("%s：得到 %v，期望通過=%v", tc.name, err, tc.ok)
		}
	}
	requirePrincipal.Store(true)
	defer requirePrincipal.Store(false)
	if err := authorizeTool("x", market(), apikey.Principal{}, false); err == nil {
		t.Fatal("正式環境沒有身分應被拒")
	}
}
