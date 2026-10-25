// Package stock 測試持股再平衡與目標配置之 MCP 工具與 API Client。
package stock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeRebalanceQuerier struct {
	fakeQuerier
	rebalanceResult *PortfolioRebalance
	targetsResult   *PortfolioTargets
	err             error
	lastRebalance   RebalanceOptions
	lastSetTargets  SetTargetsOptions
	lastGetTargets  struct {
		memberID  int64
		dimension string
	}
}

func (f *fakeRebalanceQuerier) PortfolioRebalance(_ context.Context, opt RebalanceOptions) (*PortfolioRebalance, error) {
	f.lastRebalance = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.rebalanceResult, nil
}

func (f *fakeRebalanceQuerier) SetPortfolioTargets(_ context.Context, opt SetTargetsOptions) (*PortfolioTargets, error) {
	f.lastSetTargets = opt
	if f.err != nil {
		return nil, f.err
	}
	return f.targetsResult, nil
}

func (f *fakeRebalanceQuerier) GetPortfolioTargets(_ context.Context, memberID int64, dimension string) (*PortfolioTargets, error) {
	f.lastGetTargets.memberID = memberID
	f.lastGetTargets.dimension = dimension
	if f.err != nil {
		return nil, f.err
	}
	return f.targetsResult, nil
}

func sampleRebalanceStock() *PortfolioRebalance {
	return &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "stock",
		TotalEquityValue:    5000000.0,
		TotalPortfolioValue: 6000000.0,
		TargetCashPercent:   10.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		StockItems: []StockRebalanceItem{
			{
				Symbol:           "2330",
				Name:             "台積電",
				CurrentShares:    5000,
				CurrentPrice:     1000.0,
				CurrentValue:     5000000.0,
				CurrentWeight:    83.33,
				TargetWeight:     60.0,
				DeviationPercent: -23.33,
				Action:           "sell",
				SuggestedShares:  1000,
				SuggestedAmount:  1000000.0,
				EstimatedCost:    4425.0,
				NetAmount:        995575.0,
			},
			{
				Symbol:           "2317",
				Name:             "鴻海",
				CurrentShares:    0,
				CurrentPrice:     200.0,
				CurrentValue:     0.0,
				CurrentWeight:    0.0,
				TargetWeight:     30.0,
				DeviationPercent: 30.0,
				Action:           "buy",
				SuggestedShares:  9000,
				SuggestedAmount:  1800000.0,
				EstimatedCost:    2565.0,
				NetAmount:        1802565.0,
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:            1000000.0,
			EstimatedSellNet:       995575.0,
			EstimatedBuyTotal:      1802565.0,
			AvailableCapital:       1995575.0,
			RequiredAdditionalCash: 0.0,
			RemainingCash:          193010.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}
}

// TestGetPortfolioRebalance_StockDimension 驗證個股維度試算文字摘要與免責聲明。
func TestGetPortfolioRebalance_StockDimension(t *testing.T) {
	fake := &fakeRebalanceQuerier{
		rebalanceResult: sampleRebalanceStock(),
	}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, out, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension:        "stock",
		LotSize:          1000,
		TolerancePercent: 2.0,
		AvailableCash:    1000000.0,
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "持股再平衡試算建議") {
		t.Errorf("摘要缺少標題: %s", content)
	}
	if !strings.Contains(content, "2330 台積電") || !strings.Contains(content, "賣出") {
		t.Errorf("缺少 2330 賣出建議: %s", content)
	}
	if !strings.Contains(content, "2317 鴻海") || !strings.Contains(content, "買入") {
		t.Errorf("缺少 2317 買入建議: %s", content)
	}
	if !strings.Contains(content, RebalanceDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}

	output, ok := out.(PortfolioRebalanceOutput)
	if !ok || output.Disclaimer != RebalanceDisclaimer {
		t.Errorf("結構化輸出免責聲明不符: %+v", out)
	}
	if output.PortfolioRebalance.TotalPortfolioValue != 6000000.0 {
		t.Errorf("總資產不符: %f", output.PortfolioRebalance.TotalPortfolioValue)
	}
}

// TestGetPortfolioRebalance_IndustryDimension 驗證產業維度試算輸出。
func TestGetPortfolioRebalance_IndustryDimension(t *testing.T) {
	sample := &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "industry",
		TotalEquityValue:    1000000.0,
		TotalPortfolioValue: 1000000.0,
		TargetCashPercent:   0.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		IndustryItems: []IndustryRebalanceItem{
			{
				Industry:         "半導體業",
				CurrentValue:     1000000.0,
				CurrentWeight:    100.0,
				TargetWeight:     50.0,
				DeviationPercent: -50.0,
				CandidateStocks:  []string{"2330"},
			},
			{
				Industry:         "電腦及週邊設備業",
				CurrentValue:     0.0,
				CurrentWeight:    0.0,
				TargetWeight:     50.0,
				DeviationPercent: 50.0,
				CandidateStocks:  []string{},
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:      0.0,
			AvailableCapital: 0.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}

	fake := &fakeRebalanceQuerier{rebalanceResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, _, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension: "industry",
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "產業配置偏離") {
		t.Errorf("缺少產業配置偏離標題: %s", content)
	}
	if !strings.Contains(content, "半導體業") || !strings.Contains(content, "2330") {
		t.Errorf("缺少半導體業候選標的: %s", content)
	}
	if !strings.Contains(content, RebalanceDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}
}

// TestGetPortfolioRebalance_CashShortage 驗證現金不足時顯示需新增資金。
func TestGetPortfolioRebalance_CashShortage(t *testing.T) {
	sample := &PortfolioRebalance{
		MemberID:            0,
		Dimension:           "stock",
		TotalEquityValue:    0.0,
		TotalPortfolioValue: 500000.0,
		TargetCashPercent:   0.0,
		TolerancePercent:    2.0,
		LotSize:             1000,
		StockItems: []StockRebalanceItem{
			{
				Symbol:          "2330",
				Name:            "台積電",
				CurrentShares:   0,
				CurrentPrice:    1000.0,
				Action:          "buy",
				SuggestedShares: 1000,
				SuggestedAmount: 1000000.0,
			},
		},
		CashSummary: RebalanceCashSummary{
			InitialCash:            500000.0,
			AvailableCapital:       500000.0,
			EstimatedBuyTotal:      1001425.0,
			RequiredAdditionalCash: 501425.0,
			RemainingCash:          0.0,
		},
		Disclaimer: RebalanceDisclaimer,
	}

	fake := &fakeRebalanceQuerier{rebalanceResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, _, err := ts.getPortfolioRebalance(context.Background(), nil, RebalanceInput{
		Dimension:     "stock",
		AvailableCash: 500000.0,
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "需額外新增資金") {
		t.Errorf("現金不足時應包含需額外新增資金提示: %s", content)
	}
}

// TestSetPortfolioTargets_Validation 驗證目標配置參數檢查。
func TestSetPortfolioTargets_Validation(t *testing.T) {
	fake := &fakeRebalanceQuerier{}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	// 1. 權重總和超過 100%
	_, _, err := ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 70.0},
			{TargetKey: "2317", TargetPercent: 40.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "不得超過 100%") {
		t.Errorf("應拒絕總和超過 100%%，實際: %v", err)
	}

	// 2. 負值權重
	_, _, err = ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: -5.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "必須介於 0% 至 100%") {
		t.Errorf("應拒絕負數權重，實際: %v", err)
	}

	// 3. 不合法維度
	_, _, err = ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "invalid_dim",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 50.0},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "必須為 stock") {
		t.Errorf("應拒絕不合法維度，實際: %v", err)
	}
}

// TestSetPortfolioTargets_Success 驗證設定成功與免責聲明。
func TestSetPortfolioTargets_Success(t *testing.T) {
	sample := &PortfolioTargets{
		MemberID:  0,
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 60.0},
			{TargetKey: "2317", TargetPercent: 30.0},
		},
		TargetCashPercent: 10.0,
		Disclaimer:        TargetDisclaimer,
	}

	fake := &fakeRebalanceQuerier{targetsResult: sample}
	ts := &portfolioRebalanceToolset{pr: fake, logf: t.Logf}

	res, out, err := ts.setPortfolioTargets(context.Background(), nil, SetTargetsInput{
		Dimension: "stock",
		Targets: []PortfolioTargetItem{
			{TargetKey: "2330", TargetPercent: 60.0},
			{TargetKey: "2317", TargetPercent: 30.0},
		},
	})
	if err != nil {
		t.Fatalf("非預期的錯誤: %v", err)
	}

	content := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "目標配置設定完成") {
		t.Errorf("摘要缺少設定完成文字: %s", content)
	}
	if !strings.Contains(content, "2330: 60.00%") || !strings.Contains(content, "2317: 30.00%") {
		t.Errorf("缺少標的目標比例: %s", content)
	}
	if !strings.Contains(content, TargetDisclaimer) {
		t.Errorf("缺少免責聲明: %s", content)
	}

	output := out.(PortfolioTargetsOutput)
	if output.Disclaimer != TargetDisclaimer {
		t.Errorf("結構化輸出免責聲明不符: %+v", out)
	}
}

// TestAPIClient_PortfolioRebalance_And_Targets 測試 APIClient 與 HTTP 端點通訊。
func TestAPIClient_PortfolioRebalance_And_Targets(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/portfolio/rebalance":
			if r.URL.Query().Get("dimension") != "stock" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sampleRebalanceStock())

		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/portfolio/targets":
			var payload struct {
				Dimension string                `json:"dimension"`
				Targets   []PortfolioTargetItem `json:"targets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			res := PortfolioTargets{
				MemberID:          0,
				Dimension:         payload.Dimension,
				Targets:           payload.Targets,
				TargetCashPercent: 10.0,
				Disclaimer:        TargetDisclaimer,
			}
			_ = json.NewEncoder(w).Encode(res)

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/portfolio/targets":
			res := PortfolioTargets{
				MemberID:          0,
				Dimension:         "stock",
				Targets:           []PortfolioTargetItem{{TargetKey: "2330", TargetPercent: 100.0}},
				TargetCashPercent: 0.0,
				Disclaimer:        TargetDisclaimer,
			}
			_ = json.NewEncoder(w).Encode(res)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := NewAPIClient(ts.URL, "test-key", time.Second)

	// 1. PortfolioRebalance
	reb, err := client.PortfolioRebalance(context.Background(), RebalanceOptions{
		Dimension:        "stock",
		LotSize:          1000,
		TolerancePercent: 2.0,
	})
	if err != nil {
		t.Fatalf("PortfolioRebalance 失敗: %v", err)
	}
	if reb.TotalPortfolioValue != 6000000.0 {
		t.Errorf("PortfolioRebalance 數值錯誤: %f", reb.TotalPortfolioValue)
	}

	// 2. SetPortfolioTargets
	setRes, err := client.SetPortfolioTargets(context.Background(), SetTargetsOptions{
		Dimension: "stock",
		Targets:   []PortfolioTargetItem{{TargetKey: "2330", TargetPercent: 90.0}},
	})
	if err != nil {
		t.Fatalf("SetPortfolioTargets 失敗: %v", err)
	}
	if len(setRes.Targets) != 1 || setRes.Targets[0].TargetPercent != 90.0 {
		t.Errorf("SetPortfolioTargets 回傳內容錯誤: %+v", setRes)
	}

	// 3. GetPortfolioTargets
	getRes, err := client.GetPortfolioTargets(context.Background(), 0, "stock")
	if err != nil {
		t.Fatalf("GetPortfolioTargets 失敗: %v", err)
	}
	if len(getRes.Targets) != 1 || getRes.Targets[0].TargetKey != "2330" {
		t.Errorf("GetPortfolioTargets 回傳內容錯誤: %+v", getRes)
	}
}
