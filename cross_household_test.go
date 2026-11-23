package main

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"stockmcp/apikey"
	"stockmcp/config"
	"stockmcp/stock"
	"stockmcp/web"
)

type householdTestAuth map[string]apikey.Principal

func (a householdTestAuth) Authenticate(_ context.Context, token string) (apikey.Principal, bool) {
	principal, ok := a[token]
	return principal, ok
}

// 每個 Personal 工具都用 SDK 走 HTTP 及真正的工具政策。上游只記錄隔離夾具的請求。
func TestCrossHouseholdPersonalToolCatalogOverHTTP(t *testing.T) {
	upstream, takeCalls := fakeDataAPI(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "test-households", Version: "1"}, nil)
	stock.AddTools(srv, stock.NewAPIClient(upstream, "market-fixture", time.Second).WithPersonalKey("personal-fixture"), nil)
	const rootToken, householdBToken = "smcp_test-root-a", "smcp_test-member-b"
	grants := map[string]string{
		stock.PermPortfolioView: "household", stock.PermPortfolioEdit: "household",
		stock.PermMoneyHistoryView: "household", stock.PermInventoryView: "household",
	}
	auth := householdTestAuth{
		rootToken:       {KeyID: "root-a", MemberID: 11, HouseholdID: 2, Household: []int64{11, 12}, Grants: grants},
		householdBToken: {KeyID: "member-b", MemberID: 21, HouseholdID: 3, Household: []int64{21, 22}, Grants: grants},
	}
	cfg := &config.Config{Host: "127.0.0.1", MCPPath: "/mcp", RateLimitWindow: time.Minute, RateLimitMax: 1000, TrustedProxyHops: 1}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := web.NewHandlerWithAPIKeys(cfg, logger, withPrincipalTokenInfo(newMCPHandler(srv)), nil, nil, web.WithAuthenticator(auth))
	base, listener := startTestServer(t, handler)
	t.Cleanup(func() { _ = listener.Close() })
	connect := func(token string) *mcp.ClientSession {
		t.Helper()
		session, err := mcp.NewClient(&mcp.Implementation{Name: "household-test", Version: "1"}, nil).Connect(t.Context(),
			&mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{key: token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	root, familyB := connect(rootToken), connect(householdBToken)
	total, personal := 0, 0
	for tool, err := range root.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		total++
		policy, ok := stock.PolicyOf(tool.Name)
		if !ok {
			t.Fatalf("%s 缺工具政策", tool.Name)
		}
		if !policy.Personal {
			continue
		}
		personal++
		t.Run(tool.Name, func(t *testing.T) {
			prototype := map[string]any{}
			switch tool.Name {
			case "list_portfolio_target_revisions":
				prototype["dimension"] = "stock"
			case "set_portfolio_targets":
				prototype["dimension"] = "stock"
				prototype["targets"] = []map[string]any{{"target_key": "2330", "target_percent": 50}}
			case "restore_portfolio_target_revision":
				prototype["revision_id"] = 1
			}
			for _, test := range []struct {
				session         *mcp.ClientSession
				principal       apikey.Principal
				member, foreign int64
			}{
				{root, auth[rootToken], 12, 21},
				{familyB, auth[householdBToken], 22, 11},
			} {
				args := maps.Clone(prototype)
				args["member_id"] = test.foreign
				res, err := test.session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
				if err != nil || !res.IsError {
					t.Fatalf("跨家庭的會員 %d 應被工具拒絕：%+v %v", test.foreign, res, err)
				}
				rejectedForHousehold := false
				for _, content := range res.Content {
					if message, ok := content.(*mcp.TextContent); ok && strings.Contains(message.Text, "不屬於此 Token 的家庭") {
						rejectedForHousehold = true
					}
				}
				if !rejectedForHousehold {
					t.Fatalf("應由家庭範圍檢查拒絕：%+v", res.Content)
				}
				if len(takeCalls()) != 0 {
					t.Fatal("跨家庭的請求不可到達上游")
				}
				args["member_id"] = test.member
				res, err = test.session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
				if err != nil || res.IsError {
					t.Fatalf("同家庭會員 %d 應可使用：%+v %v", test.member, res, err)
				}
				calls := takeCalls()
				if len(calls) == 0 {
					t.Fatal("同家庭請求應到達上游")
				}
				for _, call := range calls {
					if call.auth != "Bearer personal-fixture" {
						t.Fatalf("持股類工具須使用個人資料的上游金鑰：%s", call.path)
					}
					if call.method == http.MethodGet {
						query, err := url.ParseQuery(call.query)
						if err != nil || query.Get("member_id") != strconv.FormatInt(test.member, 10) ||
							query.Get("household_id") != strconv.FormatInt(test.principal.HouseholdID, 10) {
							t.Errorf("查詢範圍不符：%s %s", call.path, call.query)
						}
					} else if call.body["member_id"] != float64(test.member) ||
						call.body["household_id"] != float64(test.principal.HouseholdID) ||
						call.body["actor_source"] != "mcp" || call.body["actor_ref"] != test.principal.KeyID {
						t.Errorf("寫入範圍或來源不符：%s %v", call.path, call.body)
					}
				}
			}
		})
	}
	if total != 74 || personal != 15 {
		t.Fatalf("應驗收完整工具目錄，得到 %d 個工具，其中 %d 個屬個人資料", total, personal)
	}
}
