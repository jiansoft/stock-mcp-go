// Package stock 的 get_stock_timeline 工具:個股事件時間軸。
//
// 資料來自 stock_rust Data API 的 GET /api/v1/stocks/{symbol}/timeline:除權息與股利發放、減資／分割、
// 月營收與營收創歷史新高、季報與三率三升／三降、技術訊號(含相對強度穿越與 52 週新高新低)、外資／投信連續買賣超里程碑與董監持股變化,
// 依日期合併成一條時間軸,另列迄日之後已公告的除權息、發放與減資。
package stock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// timelineKinds 是時間軸的事件種類,與 Data API 一致(也是同一天的排列順序)。
var timelineKinds = []string{
	"ex_dividend", "ex_right", "cash_payable", "stock_payable", "corporate_action",
	"monthly_revenue", "revenue_record", "quarterly_report", "margin_trend", "signal", "chip_streak", "insider",
}

// timelineKindLabels 是事件種類的中文名稱。
var timelineKindLabels = map[string]string{
	"ex_dividend": "除息", "ex_right": "除權", "cash_payable": "現金股利發放", "stock_payable": "股票股利發放",
	"corporate_action": "減資/分割", "monthly_revenue": "月營收", "revenue_record": "營收新高", "quarterly_report": "季報",
	"margin_trend": "三率三升/三降", "signal": "技術訊號", "chip_streak": "法人連續買賣超", "insider": "董監持股",
}

// TimelineEvent 是時間軸上的一筆事件。
type TimelineEvent struct {
	Date     string  `json:"date"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Detail   *string `json:"detail"`
	Period   *string `json:"period"`
	Upcoming bool    `json:"upcoming"`
}

// TimelineKindCount 是某種事件的筆數(不受 limit 影響)。
type TimelineKindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// StockTimelineEnvelope 是 stocks/{symbol}/timeline endpoint 的完整回應。
type StockTimelineEnvelope struct {
	StockSymbol string              `json:"stock_symbol"`
	Name        *string             `json:"name"`
	From        string              `json:"from"`
	To          string              `json:"to"`
	UpcomingTo  string              `json:"upcoming_to"`
	Total       int                 `json:"total"`
	Counts      []TimelineKindCount `json:"counts"`
	Events      []TimelineEvent     `json:"events"`
}

// StockTimelineOptions 是時間軸的查詢條件;Date、Kinds 空值代表預設,UpcomingDays 為 nil 代表用 API 預設(60)。
type StockTimelineOptions struct {
	Date         string
	Days         int
	UpcomingDays *int
	Kinds        []string
	Limit        int
}

// StockTimelineQuerier 是 get_stock_timeline 對資料來源的需求介面;只有 *APIClient 實作。
type StockTimelineQuerier interface {
	StockTimeline(ctx context.Context, symbol string, opt StockTimelineOptions) (*StockTimelineEnvelope, error)
}

// StockTimeline 查詢個股事件時間軸;404 代表股票不存在,期間內沒有事件是空清單。
func (c *APIClient) StockTimeline(ctx context.Context, symbol string, opt StockTimelineOptions) (*StockTimelineEnvelope, error) {
	values := url.Values{"days": []string{fmt.Sprint(opt.Days)}, "limit": []string{fmt.Sprint(opt.Limit)}}
	if opt.Date != "" {
		values.Set("date", opt.Date)
	}
	if opt.UpcomingDays != nil {
		values.Set("upcoming_days", fmt.Sprint(*opt.UpcomingDays))
	}
	if len(opt.Kinds) > 0 {
		values.Set("kinds", strings.Join(opt.Kinds, ","))
	}
	var body StockTimelineEnvelope
	found, err := c.getFound(ctx, "/api/v1/stocks/"+url.PathEscape(symbol)+"/timeline?"+values.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrStockNotFound
	}
	return &body, nil
}

// StockTimelineInput 是 get_stock_timeline 的輸入;UpcomingDays 用指標區分「沒給」與 0(不列已公告事件)。
type StockTimelineInput struct {
	Symbol       string   `json:"symbol"`
	Date         string   `json:"date,omitempty"`
	Days         int      `json:"days,omitempty"`
	UpcomingDays *int     `json:"upcoming_days,omitempty"`
	Kinds        []string `json:"kinds,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

// StockTimelineOutput 是時間軸 envelope 加上 MCP 分析中繼資料。
type StockTimelineOutput struct {
	DataKind   string `json:"data_kind"`
	IsRealtime bool   `json:"is_realtime"`
	Disclaimer string `json:"disclaimer"`
	*StockTimelineEnvelope
}

// stockTimelineSchema 描述 get_stock_timeline 的輸入形狀。
func stockTimelineSchema() *jsonschema.Schema {
	kinds := make([]any, 0, len(timelineKinds))
	for _, kind := range timelineKinds {
		kinds = append(kinds, kind)
	}
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"symbol": symbolSchema(),
			"date": {
				Type:        "string",
				Pattern:     `^\d{4}-\d{2}-\d{2}$`,
				Description: "迄日(格式 YYYY-MM-DD,選填);未提供為今天",
			},
			"days": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(730.0),
				Description: "回看天數(日曆天,選填,預設 90,範圍 1 至 730)",
			},
			"upcoming_days": {
				Type:        "integer",
				Minimum:     ptr(0.0),
				Maximum:     ptr(180.0),
				Description: "另外列出迄日之後幾天內已公告的除權息、股利發放與減資(選填,預設 60,範圍 0 至 180;0 為不列)",
			},
			"kinds": {
				Type:  "array",
				Items: &jsonschema.Schema{Type: "string", Enum: kinds},
				Description: "只列這些種類(選填,省略為全部):ex_dividend 除息、ex_right 除權、cash_payable 現金股利發放、" +
					"stock_payable 股票股利發放、corporate_action 減資/分割、monthly_revenue 月營收、revenue_record 月營收創歷史新高、" +
					"quarterly_report 季報、margin_trend 三率三升/三降、" +
					"signal 技術訊號、chip_streak 外資/投信連續買賣超達 5/10/20 天、insider 董監持股或質押比例單月變化達 1%。" +
					"技術訊號通常占大多數,只想看基本面與籌碼大事時可排除 signal",
			},
			"limit": {
				Type:        "integer",
				Minimum:     ptr(1.0),
				Maximum:     ptr(500.0),
				Description: "最多幾筆(選填,預設 100,範圍 1 至 500),由新到舊;各種類的總筆數另外列出、不受此限",
			},
		},
		Required: []string{"symbol"},
	}
}

// normalizeTimelineKinds 去掉空白與重複,只接受固定種類。
func normalizeTimelineKinds(raw []string) ([]string, error) {
	kinds := make([]string, 0, len(raw))
	for _, kind := range raw {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" || slices.Contains(kinds, kind) {
			continue
		}
		if !slices.Contains(timelineKinds, kind) {
			return nil, fmt.Errorf("參數 kinds 只支援 %s,收到了 %q", strings.Join(timelineKinds, "、"), kind)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

// stockTimelineToolset 綁定時間軸資料來源與記錄函式。
type stockTimelineToolset struct {
	tq   StockTimelineQuerier
	logf func(string, ...any)
}

// stockTimeline 執行 get_stock_timeline。
func (ts *stockTimelineToolset) stockTimeline(ctx context.Context, _ *mcp.CallToolRequest, in StockTimelineInput) (*mcp.CallToolResult, any, error) {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return nil, nil, err
	}
	date, err := parseOptionalDate(in.Date, "date")
	if err != nil {
		return nil, nil, err
	}
	days, err := rangedLimit(in.Days, 90, 1, 730)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 days 必須介於 1 到 730 之間")
	}
	if in.UpcomingDays != nil && (*in.UpcomingDays < 0 || *in.UpcomingDays > 180) {
		return nil, nil, fmt.Errorf("參數 upcoming_days 必須介於 0 到 180 之間")
	}
	kinds, err := normalizeTimelineKinds(in.Kinds)
	if err != nil {
		return nil, nil, err
	}
	limit, err := rangedLimit(in.Limit, 100, 1, 500)
	if err != nil {
		return nil, nil, fmt.Errorf("參數 limit 必須介於 1 到 500 之間")
	}
	envelope, err := ts.tq.StockTimeline(ctx, symbol, StockTimelineOptions{
		Date: date, Days: days, UpcomingDays: in.UpcomingDays, Kinds: kinds, Limit: limit,
	})
	if err != nil {
		if errors.Is(err, ErrStockNotFound) {
			return nil, nil, fmt.Errorf("找不到股票代號:%s", in.Symbol)
		}
		ts.logf("工具 get_stock_timeline 執行失敗:%v", err)
		return nil, nil, fmt.Errorf("%s", errInternal)
	}
	return textResult(stockTimelineSummary(envelope)), StockTimelineOutput{
		DataKind: "stock_timeline", IsRealtime: false, Disclaimer: AnalysisDisclaimer, StockTimelineEnvelope: envelope,
	}, nil
}

// stockTimelineSummary 組出時間軸摘要:各種類筆數一行,事件一行一筆(由新到舊)。
func stockTimelineSummary(e *StockTimelineEnvelope) string {
	var b summaryBuilder
	name := ""
	if e.Name != nil {
		name = " " + *e.Name
	}
	b.printf("股票 %s%s %s～%s 事件時間軸", e.StockSymbol, name, e.From, e.To)
	if e.UpcomingTo != e.To {
		b.printf("(另列至 %s 已公告的除權息、發放與減資)", e.UpcomingTo)
	}
	if e.Total == 0 {
		b.printf(":期間內沒有事件。")
		b.printf("\n免責聲明:%s", AnalysisDisclaimer)
		return b.String()
	}
	counts := make([]string, 0, len(e.Counts))
	for _, count := range e.Counts {
		label := timelineKindLabels[count.Kind]
		if label == "" {
			label = count.Kind
		}
		counts = append(counts, fmt.Sprintf("%s %d", label, count.Count))
	}
	b.printf(":共 %d 筆(%s)", e.Total, strings.Join(counts, "、"))
	if len(e.Events) < e.Total {
		b.printf(",列出最新 %d 筆", len(e.Events))
	}
	b.printf("。月營收與季報的日期是法定公布期限,董監持股為申報期限。")
	for _, event := range e.Events {
		upcoming := ""
		if event.Upcoming {
			upcoming = "【已公告、尚未發生】"
		}
		detail := ""
		if event.Detail != nil && *event.Detail != "" {
			detail = "(" + *event.Detail + ")"
		}
		b.printf("\n- %s %s%s%s", event.Date, upcoming, event.Title, detail)
	}
	b.printf("\n免責聲明:%s", AnalysisDisclaimer)
	return b.String()
}
