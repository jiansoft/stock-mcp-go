package stock

import "errors"

var (
	// ErrStockNotFound 明確表示股票主檔不存在，讓工具層能與「存在但區間無資料」分辨。
	ErrStockNotFound = errors.New("stock not found")
	// ErrMarketDataNotFound 表示市場分析表在指定回溯窗口內沒有任何資料。
	//
	// 這是可理解的正常資料狀態，不是認證、連線或伺服器故障；tool 層因此
	// 可以回明確的「查無資料」，而不會誤報成未預期內部錯誤。
	ErrMarketDataNotFound = errors.New("market data not found")
)
