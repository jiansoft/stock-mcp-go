// Package stock 測試目標配置版本工具(list_portfolio_target_revisions／restore_portfolio_target_revision)。
package stock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// revisionServer 模擬 Data API 的版本清單與還原端點,記下最後一次請求。
func revisionServer(t *testing.T, restoreStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/portfolio/targets/revisions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"member_id": 7, "dimension": r.URL.Query().Get("dimension"),
				"revisions": []map[string]any{
					{"id": 12, "saved_time": "2026-10-28T15:30:00+00:00", "target_cash_percent": 24.5,
						"targets": []map[string]any{{"target_key": "半導體業", "target_percent": 60}, {"target_key": "金融保險業", "target_percent": 15.5}}},
					{"id": 11, "saved_time": "bad-time", "target_cash_percent": 100, "targets": []map[string]any{}},
					{"id": 10, "saved_time": "2026-10-27T01:00:00Z", "target_cash_percent": 4,
						"targets": []map[string]any{
							{"target_key": "A", "target_percent": 20}, {"target_key": "B", "target_percent": 20},
							{"target_key": "C", "target_percent": 20}, {"target_key": "D", "target_percent": 20},
							{"target_key": "E", "target_percent": 10}, {"target_key": "F", "target_percent": 6},
						}},
				},
				"disclaimer": TargetDisclaimer,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/portfolio/targets/revisions/"):
			if restoreStatus != http.StatusOK {
				w.WriteHeader(restoreStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"member_id": 7, "dimension": "industry", "target_cash_percent": 24.5,
				"targets":                   []map[string]any{{"dimension": "industry", "target_key": "半導體業", "target_percent": 60}},
				"cash_percent_by_dimension": map[string]float64{"industry": 24.5},
				"disclaimer":                TargetDisclaimer,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// resultText 取出工具回傳的摘要文字。
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("工具結果不是文字:%T", result.Content[0])
	}
	return text.Text
}

func revisionToolset(server *httptest.Server) *portfolioTargetRevisionToolset {
	return &portfolioTargetRevisionToolset{
		rq:   NewAPIClient(server.URL, "test-key", 5*time.Second),
		logf: func(string, ...any) {},
	}
}

func TestListTargetRevisionsSummarizesEachVersion(t *testing.T) {
	server, calls := revisionServer(t, http.StatusOK)
	result, output, err := revisionToolset(server).listTargetRevisions(t.Context(), nil, ListTargetRevisionsInput{MemberID: 7, Dimension: "industry"})
	if err != nil {
		t.Fatalf("list:%v", err)
	}
	if got := (*calls)[0]; got != "GET /api/v1/portfolio/targets/revisions?dimension=industry&member_id=7 " {
		t.Fatalf("請求 %q", got)
	}
	text := resultText(t, result)
	for _, want := range []string{
		"目標配置歷史版本(維度: 產業 | 會員: 7),共 3 版",
		"#12 2026-10-28 23:30:半導體業 60.00%、金融保險業 15.50%;現金 24.50%",
		"#11 bad-time:全現金",
		"#10 2026-10-27 09:00:A 20.00%、B 20.00%、C 20.00%、D 20.00%、E 10.00% 等 6 檔;現金 4.00%",
		"restore_portfolio_target_revision",
		TargetDisclaimer,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("摘要缺少 %q:\n%s", want, text)
		}
	}
	out := output.(PortfolioTargetRevisionsOutput)
	if out.DataKind != "portfolio_target_revisions" || len(out.Revisions) != 3 {
		t.Fatalf("輸出 %+v", out)
	}
}

func TestListTargetRevisionsEmptyAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"member_id":0,"dimension":"stock","revisions":null,"disclaimer":""}`))
	}))
	t.Cleanup(server.Close)
	ts := revisionToolset(server)
	result, output, err := ts.listTargetRevisions(t.Context(), nil, ListTargetRevisionsInput{Dimension: "stock"})
	if err != nil {
		t.Fatalf("list:%v", err)
	}
	if !strings.Contains(resultText(t, result), "目前沒有任何版本") {
		t.Fatalf("空清單摘要:%s", resultText(t, result))
	}
	if revisions := output.(PortfolioTargetRevisionsOutput).Revisions; revisions == nil || len(revisions) != 0 {
		t.Fatalf("空清單應為 [] 而非 null:%v", revisions)
	}
	for _, in := range []ListTargetRevisionsInput{{MemberID: -1, Dimension: "stock"}, {Dimension: ""}, {Dimension: "bad"}} {
		if _, _, err := ts.listTargetRevisions(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
}

func TestRestoreTargetRevisionPostsMemberAndReportsResult(t *testing.T) {
	server, calls := revisionServer(t, http.StatusOK)
	result, output, err := revisionToolset(server).restoreTargetRevision(t.Context(), nil, RestoreTargetRevisionInput{MemberID: 7, RevisionID: 12})
	if err != nil {
		t.Fatalf("restore:%v", err)
	}
	if got := (*calls)[0]; got != `POST /api/v1/portfolio/targets/revisions/12/restore {"member_id":7}` {
		t.Fatalf("請求 %q", got)
	}
	text := resultText(t, result)
	if !strings.Contains(text, "已還原到版本 #12") || !strings.Contains(text, "半導體業: 60.00%") {
		t.Fatalf("還原摘要:%s", text)
	}
	if out := output.(PortfolioTargetsOutput); out.DataKind != "portfolio_targets" || len(out.Targets) != 1 {
		t.Fatalf("輸出 %+v", out)
	}
}

func TestRestoreTargetRevisionErrors(t *testing.T) {
	notFound, _ := revisionServer(t, http.StatusNotFound)
	_, _, err := revisionToolset(notFound).restoreTargetRevision(t.Context(), nil, RestoreTargetRevisionInput{MemberID: 7, RevisionID: 99})
	if err == nil || !strings.Contains(err.Error(), "找不到會員 7 的版本 99") {
		t.Fatalf("404 應轉成找不到版本:%v", err)
	}
	broken, _ := revisionServer(t, http.StatusInternalServerError)
	_, _, err = revisionToolset(broken).restoreTargetRevision(t.Context(), nil, RestoreTargetRevisionInput{RevisionID: 1})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("500 應原樣回報:%v", err)
	}
	for _, in := range []RestoreTargetRevisionInput{{MemberID: -1, RevisionID: 1}, {RevisionID: 0}} {
		if _, _, err := revisionToolset(broken).restoreTargetRevision(t.Context(), nil, in); err == nil {
			t.Errorf("%+v 應回錯誤", in)
		}
	}
}

func TestRevisionHelpers(t *testing.T) {
	if got := targetDimensionName("etf_look_through"); got != "ETF 穿透" {
		t.Errorf("etf_look_through → %q", got)
	}
	if got := targetDimensionName("other"); got != "other" {
		t.Errorf("未知維度應原樣輸出:%q", got)
	}
}

func TestTargetRevisionClientEdgeCases(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	if _, _, err := revisionToolset(failing).listTargetRevisions(t.Context(), nil, ListTargetRevisionsInput{Dimension: "stock"}); err == nil {
		t.Fatal("列出失敗應回錯誤")
	}
	nullTargets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"member_id":0,"dimension":"stock","targets":null,"target_cash_percent":100,"disclaimer":""}`))
	}))
	t.Cleanup(nullTargets.Close)
	result, err := NewAPIClient(nullTargets.URL, "k", time.Second).RestorePortfolioTargetRevision(t.Context(), 0, 3)
	if err != nil || result.Targets == nil || len(result.Targets) != 0 {
		t.Fatalf("全現金版本還原後 targets 應為 []:%+v %v", result, err)
	}
}
