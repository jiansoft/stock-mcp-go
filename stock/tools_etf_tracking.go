package stock

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EtfProxyTracking 保留代理基準、原追蹤指數及有效樣本資訊；缺資料的指標維持 nil。
type EtfProxyTracking struct {
	StockSymbol               string   `json:"stock_symbol"`
	BenchmarkSymbol           string   `json:"benchmark_symbol"`
	BenchmarkKind             string   `json:"benchmark_kind"`
	TargetBenchmark           string   `json:"target_benchmark"`
	DataAsOf                  *string  `json:"data_as_of"`
	Samples                   int      `json:"samples"`
	TrackingErrorPercent      *float64 `json:"tracking_error_percent"`
	TrackingDifferencePercent *float64 `json:"tracking_difference_percent"`
	Note                      string   `json:"note"`
}

type EtfTrackingQuerier interface {
	EtfProxyTracking(context.Context, string) (*EtfProxyTracking, error)
}

func (c *APIClient) EtfProxyTracking(ctx context.Context, symbol string) (*EtfProxyTracking, error) {
	var data EtfProxyTracking
	found, err := c.getFound(ctx, "/api/v1/etfs/"+url.PathEscape(symbol)+"/tracking-error", &data)
	if err != nil || !found {
		return nil, err
	}
	return &data, nil
}

func addEtfTrackingTool(server *mcp.Server, q Querier, logf func(string, ...any)) {
	reader, ok := q.(EtfTrackingQuerier)
	if !ok {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "get_etf_proxy_tracking", Description: "查詢 ETF 相對 0050 的近一年代理基準報酬差異及年化報酬差波動；至少 120 組樣本，並非相對實際指數的追蹤誤差。", InputSchema: symbolOnlySchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, any, error) {
			symbol, err := normalizeSymbol(in.Symbol)
			if err != nil {
				return nil, nil, err
			}
			if len(symbol) > 12 || strings.IndexFunc(symbol, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }) >= 0 {
				return nil, nil, fmt.Errorf("ETF 代號格式不正確")
			}
			data, err := reader.EtfProxyTracking(ctx, symbol)
			if err != nil {
				logf("ETF 代理基準查詢失敗:%v", err)
				return nil, nil, fmt.Errorf("%s", errInternal)
			}
			if data == nil {
				return nil, nil, fmt.Errorf("查無 ETF %s", symbol)
			}
			out := struct {
				DataKind   string `json:"data_kind"`
				IsRealtime bool   `json:"is_realtime"`
				Disclaimer string `json:"disclaimer"`
				*EtfProxyTracking
			}{"etf_proxy_tracking", false, AnalysisDisclaimer, data}
			return textResult(etfProxyTrackingSummary(symbol, data)), out, nil
		})
}

// etfProxyTrackingSummary 組出摘要；樣本不足（Rust 尚未計算或未達 120 組）時明說，不拼出「無 個百分點」。
func etfProxyTrackingSummary(symbol string, data *EtfProxyTracking) string {
	head := fmt.Sprintf("%s 相對 %s 代理基準報酬差異（資料日 %s）：", symbol, data.BenchmarkSymbol, displayString(data.DataAsOf))
	if data.TrackingErrorPercent == nil && data.TrackingDifferencePercent == nil {
		return head + fmt.Sprintf("尚無足夠樣本（有效樣本 %d，需至少 120 組同區間日報酬）。%s %s", data.Samples, data.Note, AnalysisDisclaimer)
	}
	difference := "無"
	if data.TrackingDifferencePercent != nil {
		difference = displayFloat(data.TrackingDifferencePercent) + " 個百分點"
	}
	return head + fmt.Sprintf("有效樣本 %d，年化報酬差波動 %s，配對區間累積報酬差 %s。%s %s", data.Samples, displayPercent(data.TrackingErrorPercent), difference, data.Note, AnalysisDisclaimer)
}
