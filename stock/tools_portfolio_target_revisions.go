// Package stock 的目標配置版本工具:list_portfolio_target_revisions 與 restore_portfolio_target_revision。
//
// 呼叫 stock_rust Data API 的 GET /api/v1/portfolio/targets/revisions 與
// POST /api/v1/portfolio/targets/revisions/{id}/restore:每次設定目標配置前,該維度原本的整組會記成一版
// (每個維度保留最近 20 版);還原是用那一版的內容再設定一次,所以還原前的設定也會留成一版。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// revisionPreviewItems 是摘要裡每一版最多列出的標的數,其餘只計數。
const revisionPreviewItems = 5

// PortfolioTargetRevision 是目標配置的一個歷史版本:某次設定前的整組配置。
type PortfolioTargetRevision struct {
	ID                int64                 `json:"id"`
	SavedTime         string                `json:"saved_time"`
	Targets           []PortfolioTargetItem `json:"targets"`
	TargetCashPercent float64               `json:"target_cash_percent"`
}

// PortfolioTargetRevisions 是某會員某維度的歷史版本清單(由新到舊)。
type PortfolioTargetRevisions struct {
	MemberID   int64                     `json:"member_id"`
	Dimension  string                    `json:"dimension"`
	Revisions  []PortfolioTargetRevision `json:"revisions"`
	Disclaimer string                    `json:"disclaimer"`
}

// PortfolioTargetRevisionQuerier 是目標配置版本工具對資料來源的需求介面。
type PortfolioTargetRevisionQuerier interface {
	PortfolioTargetRevisions(ctx context.Context, memberID int64, dimension string) (*PortfolioTargetRevisions, error)
	RestorePortfolioTargetRevision(ctx context.Context, memberID, revisionID int64) (*PortfolioTargets, error)
}

// PortfolioTargetRevisions 呼叫 Data API 列出目標配置的歷史版本。
func (c *APIClient) PortfolioTargetRevisions(ctx context.Context, memberID int64, dimension string) (*PortfolioTargetRevisions, error) {
	values := url.Values{"member_id": []string{fmt.Sprint(memberID)}, "dimension": []string{dimension}}
	var body PortfolioTargetRevisions
	if err := c.get(ctx, "/api/v1/portfolio/targets/revisions?"+values.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Revisions == nil {
		body.Revisions = []PortfolioTargetRevision{}
	}
	return &body, nil
}

// RestorePortfolioTargetRevision 呼叫 Data API 把目標配置還原成某個歷史版本。
func (c *APIClient) RestorePortfolioTargetRevision(ctx context.Context, memberID, revisionID int64) (*PortfolioTargets, error) {
	payload := struct {
		MemberID int64 `json:"member_id"`
	}{MemberID: memberID}
	var body PortfolioTargets
	path := fmt.Sprintf("/api/v1/portfolio/targets/revisions/%d/restore", revisionID)
	if err := c.post(ctx, path, payload, &body); err != nil {
		return nil, err
	}
	if body.Targets == nil {
		body.Targets = []PortfolioTargetItem{}
	}
	return &body, nil
}

// ListTargetRevisionsInput 是 list_portfolio_target_revisions 的輸入參數。
type ListTargetRevisionsInput struct {
	MemberID  int64  `json:"member_id"`
	Dimension string `json:"dimension"`
}

// RestoreTargetRevisionInput 是 restore_portfolio_target_revision 的輸入參數。
type RestoreTargetRevisionInput struct {
	MemberID   int64 `json:"member_id"`
	RevisionID int64 `json:"revision_id"`
}

// PortfolioTargetRevisionsOutput 是 list_portfolio_target_revisions 的結構化輸出。
type PortfolioTargetRevisionsOutput struct {
	DataKind   string `json:"data_kind"`
	Disclaimer string `json:"disclaimer"`
	*PortfolioTargetRevisions
}

func memberIDSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     ptr(0.0),
		Default:     []byte("0"),
		Description: "會員編號; 0(預設)為所有部位",
	}
}

func listTargetRevisionsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"dimension"},
		Properties: map[string]*jsonschema.Schema{
			"dimension": {
				Type:        "string",
				Enum:        []any{"stock", "industry", "etf_look_through"},
				Description: "配置維度:stock(個股)、industry(產業)、etf_look_through(ETF 穿透)",
			},
			"member_id": memberIDSchema(),
		},
	}
}

func restoreTargetRevisionSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"revision_id"},
		Properties: map[string]*jsonschema.Schema{
			"revision_id": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Description: "要還原的版本編號(見 list_portfolio_target_revisions)",
			},
			"member_id": memberIDSchema(),
		},
	}
}

type portfolioTargetRevisionToolset struct {
	rq   PortfolioTargetRevisionQuerier
	logf func(string, ...any)
}

func (ts *portfolioTargetRevisionToolset) listTargetRevisions(ctx context.Context, _ *mcp.CallToolRequest, in ListTargetRevisionsInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	if in.Dimension != "stock" && in.Dimension != "industry" && in.Dimension != "etf_look_through" {
		return nil, nil, fmt.Errorf("參數 dimension 必須為 stock, industry, 或 etf_look_through")
	}
	result, err := ts.rq.PortfolioTargetRevisions(ctx, in.MemberID, in.Dimension)
	if err != nil {
		ts.logf("工具 list_portfolio_target_revisions 執行失敗:%v", err)
		return nil, nil, err
	}
	return textResult(formatTargetRevisionsText(result)), PortfolioTargetRevisionsOutput{
		DataKind:                 "portfolio_target_revisions",
		Disclaimer:               TargetDisclaimer,
		PortfolioTargetRevisions: result,
	}, nil
}

func (ts *portfolioTargetRevisionToolset) restoreTargetRevision(ctx context.Context, _ *mcp.CallToolRequest, in RestoreTargetRevisionInput) (*mcp.CallToolResult, any, error) {
	if in.MemberID < 0 {
		return nil, nil, fmt.Errorf("參數 member_id 不可為負數")
	}
	if in.RevisionID < 1 {
		return nil, nil, fmt.Errorf("參數 revision_id 必須為正整數")
	}
	result, err := ts.rq.RestorePortfolioTargetRevision(ctx, in.MemberID, in.RevisionID)
	if err != nil {
		var status *apiStatusError
		if errors.As(err, &status) && status.status == http.StatusNotFound {
			return nil, nil, fmt.Errorf("找不到會員 %d 的版本 %d,請先用 list_portfolio_target_revisions 查看版本編號", in.MemberID, in.RevisionID)
		}
		ts.logf("工具 restore_portfolio_target_revision 執行失敗:%v", err)
		return nil, nil, err
	}
	text := fmt.Sprintf("已還原到版本 #%d(還原前的設定已另存成一版,可再還原回去)。\n\n%s", in.RevisionID, formatTargetsText(result, true))
	return textResult(text), PortfolioTargetsOutput{
		DataKind:         "portfolio_targets",
		Disclaimer:       TargetDisclaimer,
		PortfolioTargets: result,
	}, nil
}

// targetDimensionName 是維度的中文名稱。
func targetDimensionName(dimension string) string {
	switch dimension {
	case "stock":
		return "個股"
	case "industry":
		return "產業"
	case "etf_look_through":
		return "ETF 穿透"
	}
	return dimension
}

// revisionTime 把 RFC 3339 的版本時間轉成台北時間;格式不符時原樣輸出。
func revisionTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return parsed.In(taipeiLocation).Format("2006-01-02 15:04")
}

func formatTargetRevisionsText(r *PortfolioTargetRevisions) string {
	var b summaryBuilder
	b.printf("目標配置歷史版本(維度: %s | 會員: %d)", targetDimensionName(r.Dimension), r.MemberID)
	if len(r.Revisions) == 0 {
		b.printf("\n目前沒有任何版本:第一次設定不會留版本,之後每次內容有變才會記下設定前的整組。")
	} else {
		b.printf(",共 %d 版,由新到舊(時間為被取代的時間):", len(r.Revisions))
		for _, revision := range r.Revisions {
			b.printf("\n- #%d %s:", revision.ID, revisionTime(revision.SavedTime))
			if len(revision.Targets) == 0 {
				b.printf("全現金")
				continue
			}
			items := make([]string, 0, revisionPreviewItems)
			for i, item := range revision.Targets {
				if i == revisionPreviewItems {
					break
				}
				items = append(items, fmt.Sprintf("%s %.2f%%", item.TargetKey, item.TargetPercent))
			}
			more := ""
			if len(revision.Targets) > revisionPreviewItems {
				more = fmt.Sprintf(" 等 %d 檔", len(revision.Targets))
			}
			b.printf("%s%s;現金 %.2f%%", strings.Join(items, "、"), more, revision.TargetCashPercent)
		}
		b.printf("\n要還原請用 restore_portfolio_target_revision(revision_id 為上面的 #編號)。")
	}
	b.printf("\n\n免責聲明: %s", TargetDisclaimer)
	return b.String()
}
