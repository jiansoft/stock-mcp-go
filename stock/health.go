package stock

import (
	"context"
	"fmt"
)

// HealthChecker 是資料來源可選的「健康檢查」能力:回傳 nil 代表這個資料
// 來源目前可以正常服務查詢,回傳 error 代表不行(連不上、認證失效等)。
//
// 與本套件其他能力介面(SnapshotQuerier、FinancialQuerier 等)一樣,這是
// 一個定義在使用端的小介面,由呼叫端用型別斷言偵測資料來源是否具備這個
// 能力,而不是強迫所有實作都得提供。
type HealthChecker interface {
	Health(ctx context.Context) error
}

// Health 確認 Data API 可以到達、且本服務持有的 API key 仍然有效。
//
// ## 為什麼用 search 而不是一個專門的 /health endpoint?
//
// 因為不能憑空假設 stock_rust 提供了健康檢查端點——若它其實不存在,這個
// 檢查會永遠回傳 404 而把健康的服務誤判成不健康,比完全不檢查更糟。
// search 是契約上確定存在、且是唯讀的最輕量查詢,limit=1 讓伺服器端只需
// 回傳最多一筆資料。它同時驗證了三件事:網路可達、API key 有效、後端
// 真的能回應查詢——這正是「本服務現在能不能正常服務使用者」的定義。
//
// 這裡刻意不檢查「有沒有查到資料」:查無資料是完全正常的結果,與健康
// 與否無關。只要請求成功完成且回應可解析,就算健康。
func (c *APIClient) Health(ctx context.Context) error {
	if _, err := c.SearchStock(ctx, "2330", 1); err != nil {
		return fmt.Errorf("資料 API 健康檢查失敗:%w", err)
	}
	return nil
}

// Health 確認資料庫連線池仍可取得可用連線。
//
// pgxpool 的 Ping 會從連線池借一條連線、送出一次最輕量的往返確認,能同時
// 涵蓋「連線池已耗盡」「資料庫主機不可達」「認證失效」這幾種故障。
func (r *Repository) Health(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("資料庫健康檢查失敗:%w", err)
	}
	return nil
}
