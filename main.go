// stock-mcp:以 MCP Streamable HTTP transport 提供台股資料查詢的唯讀
// 服務。這個檔案是整個程式的進入點(entry point),負責把 config、
// stock、web 三個套件「接線」(wiring)組裝起來,本身不包含任何查詢
// 邏輯或 HTTP 路由細節——那些都委派給對應的套件處理,main.go 只決定
// 「用什麼順序、用什麼設定值去建立這些元件」。
//
// 資料來源為既有 stock_rust 專案的 PostgreSQL 資料庫;所有報價均為
// 「資料庫目前最新可取得的日報價」,非交易所保證即時行情。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"stockmcp/config"
	"stockmcp/stock"
	"stockmcp/web"
)

// main 是 Go 程式真正的執行進入點:每個 `package main` 都必須有且只能
// 有一個 main 函式,程式啟動時會自動呼叫它,不需要(也不能)自己
// 呼叫它。
//
// main 本身刻意寫得很薄:只做「載入 .env」跟「呼叫 run,把結果錯誤印
// 出來並決定結束碼」這兩件事,真正的啟動邏輯都放在 run 函式裡。這樣
// 拆分的好處是 run 回傳一般的 error(而不是自己呼叫 os.Exit),測試或
// 未來想重複利用「啟動流程」時,可以直接呼叫 run 並檢查它的回傳值,
// 不會因為呼叫到 os.Exit 而把整個測試程序也一起關掉。
func main() {
	// godotenv.Load() 會嘗試讀取當前目錄的 .env 檔案,把裡面定義的
	// KEY=VALUE 逐行載入成環境變數(只在該環境變數「尚未存在」時才會
	// 設定,不會覆蓋部署環境本身已經設定好的環境變數)。這僅供本機開發
	// 使用:正式環境的環境變數應該由部署平台(Docker、systemd 等)直接
	// 注入,不依賴 .env 檔案。呼叫端刻意忽略回傳的 error(用 _ 接住)
	// ——.env 檔案不存在是完全正常的情況(例如正式環境根本不會有這個
	// 檔案),不應該因為找不到 .env 就讓程式啟動失敗。
	_ = godotenv.Load()

	if err := run(context.Background()); err != nil {
		// 這裡刻意用 fmt.Fprintln 寫到 os.Stderr,而不是用 slog——此時
		// config.Load() 可能還沒執行成功,還沒有機會決定 log 的格式與
		// 等級,直接寫標準錯誤輸出是最保險、不依賴任何尚未初始化元件的
		// 方式。err 訊息由 config 套件與其他底層邏輯保證不含敏感資訊
		// (資料庫密碼、API key 等),可以放心直接印出。
		fmt.Fprintln(os.Stderr, "啟動失敗:", err)
		os.Exit(1)
	}
}

// run 執行完整的啟動流程:載入設定 → 建立資料庫連線池 → 註冊 MCP
// 工具 → 啟動 HTTP 伺服器 → 等待關閉訊號並優雅關閉。
//
// 回傳 error 而非直接讓程式結束,方便未來若有需要,可以在測試或其他
// 呼叫情境中取得明確的錯誤結果(而不是整個測試程序被 os.Exit 中斷)。
func run(ctx context.Context) error {
	// signal.NotifyContext 回傳一個「衍生自 ctx」的新 context,以及一個
	// stop 函式。這個新 context 會在收到作業系統送來的 os.Interrupt
	// (通常是使用者按下 Ctrl+C)或 syscall.SIGTERM(容器編排工具、
	// systemd 等要求程式正常關閉時常送出的訊號)時自動被「取消」
	// (Done() channel 會被關閉)。defer stop() 確保不論 run 函式從
	// 哪個路徑返回,都會釋放這個訊號監聽器佔用的資源,避免資源洩漏。
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// slog 是 Go 標準函式庫(1.21 起)內建的結構化 log 套件,輸出的每一
	// 行 log 都是一筆 JSON,而不是傳統 log 套件那種純文字訊息——結構化
	// log 方便日後接上集中式 log 系統(例如 ELK、Loki)做查詢與篩選。
	// slog.SetDefault 把這個 logger 設成整個程式的預設 logger,讓即使
	// 是套件內部沒有明確拿到 logger 參數的地方,呼叫 slog 套件層級的
	// 函式時也會用到這個設定(不過本專案的慣例是盡量明確傳遞 logger,
	// 只在極少數無法傳遞的地方依賴這個預設值)。
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	var repo stock.Querier
	var closeDataSource func()
	if cfg.DataSource == "api" {
		repo = stock.NewAPIClient(cfg.StockRustAPIBaseURL, cfg.StockRustAPIKey, cfg.APITimeout)
		closeDataSource = func() {}
	} else {
		pool, err := newPool(ctx, cfg)
		if err != nil {
			return err
		}
		repo = stock.NewRepository(pool)
		closeDataSource = pool.Close
	}
	// defer pool.Close() 確保無論 run 函式從哪一個 return 離開,都會
	// 關閉資料庫連線池、釋放底層的網路連線,不會因為程式提前返回而讓
	// 連線一直開著。
	defer closeDataSource()

	// mcp.NewServer 建立一個空的 MCP server 實例(此時還沒有註冊任何
	// 工具),Implementation 這個欄位是給 MCP 用戶端在初始化交握
	// (initialize)時看到的「這是什麼服務」的識別資訊,並非影響行為的
	// 設定。
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "stock-mcp",
		Title:   "台股資料 MCP Server",
		Version: "0.1.0",
	}, nil)
	// stock.AddTools 把四個工具(search_stock 等)註冊到這個 server 上,
	// 見 stock/tools.go 的詳細說明。最後一個參數是「資料庫錯誤該怎麼
	// 記錄」的回呼函式,這裡選擇接到 logger.Error,把 stock 套件內部
	// 的錯誤訊息當成一般文字格式化後,用結構化 log 的 Error 等級記錄
	// 下來。
	stock.AddTools(server, repo, func(format string, args ...any) {
		logger.Error(fmt.Sprintf(format, args...))
	})

	// mcp.NewStreamableHTTPHandler 是 go-sdk 提供的轉接器:把上面建好
	// 的 *mcp.Server 包裝成一個標準的 http.Handler,讓它可以直接掛進
	// web 套件的路由裡。第一個參數是一個「依照每次 HTTP 請求動態決定
	// 要用哪個 *mcp.Server」的函式——這裡因為整個程式只有一個 server
	// 實例,所以不論傳進來的 *http.Request 是什麼,一律回傳同一個
	// server;這個彈性主要是給需要「依請求內容切換不同伺服器實例」的
	// 進階情境使用,本專案用不到。
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	)

	srv := web.NewServer(cfg, web.NewHandler(cfg, logger, mcpHandler))

	// 這裡用一個「容量為 1 的 channel」搭配一個獨立的 goroutine 啟動
	// HTTP 伺服器,而不是直接在目前這個 goroutine 呼叫
	// srv.ListenAndServe()(它是一個「阻塞」呼叫,只有在伺服器停止時
	// 才會返回),是為了讓 run 函式接下來能同時「等待伺服器啟動失敗」
	// 跟「等待作業系統的關閉訊號」這兩件事,兩者不論哪個先發生,程式
	// 都要能對應處理——這是下面 select 語句要處理的兩種情境。
	//
	// errCh 宣告成容量 1 的緩衝 channel(make(chan error, 1)),是為了
	// 避免下面的 goroutine 在寫入 errCh 時,如果沒有任何人在讀取
	// (例如 run 函式已經走了另一條路徑提前返回),會永遠卡在寫入
	// 那一行動彈不得,变成一個永遠不會結束的 goroutine(goroutine
	// 洩漏)。容量 1 讓這一次的寫入即使沒人立刻讀取也能成功完成,
	// goroutine 可以正常結束。
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("stock-mcp 已啟動",
		"addr", cfg.Addr(),
		"mcp_path", cfg.MCPPath,
		"app_env", string(cfg.AppEnv),
	)

	// select 會同時監看兩個 channel,哪一個先有資料送達,就執行對應的
	// case 分支(如果兩者「同時」有資料,Go runtime 會隨機選一個執行,
	// 但在本情境下這兩個事件幾乎不可能真的同時發生)。
	select {
	case err := <-errCh:
		// ListenAndServe 提早結束、送出了一個 error,通常代表啟動階段
		// 就失敗了(例如埠號已被佔用),這種情況直接把錯誤往外傳,讓
		// main 印出訊息並以非零結束碼結束程式。
		return fmt.Errorf("HTTP 伺服器啟動失敗:%w", err)
	case <-ctx.Done():
		// ctx.Done() 被觸發,代表收到了作業系統的關閉訊號(Ctrl+C 或
		// SIGTERM)。這裡執行「優雅關閉」(graceful shutdown):不是
		// 立刻粗暴地砍斷所有連線,而是呼叫 srv.Shutdown,它會停止接受
		// 新連線、但讓正在處理中的請求有機會正常完成。
		logger.Info("收到停止訊號,開始優雅關閉")
		// 這裡刻意用 context.Background()(全新的、沒有已被取消的
		// context)搭配一個新的 10 秒逾時,而不是繼續沿用外層已經被
		// 取消的 ctx——如果沿用已取消的 ctx,srv.Shutdown 內部一檢查
		// context 狀態就會立刻判定「已經取消」,完全沒有機會等待
		// 進行中的請求完成,graceful shutdown 就形同虛設了。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// newPool 建立 PostgreSQL 連線池:套用連線數上限、連線逾時,並以
// statement_timeout(PostgreSQL 伺服器端設定,限制單一條查詢語句最長
// 可以執行多久)限制每一條查詢的最長執行時間,避免一條異常緩慢的查詢
// (例如因為資料庫負載過高、或查詢條件不慎命中全表掃描)長時間佔用
// 連線池裡的連線,拖累其他請求。
//
// 啟動時額外呼叫一次 Ping,及早在啟動階段就發現連線設定錯誤(例如帳號
// 密碼錯誤、資料庫主機無法連線),而不是等到第一次真正查詢時才發現
// ——啟動階段失敗會讓部署工具(Docker、systemd 等)立刻知道這次啟動
// 失敗,比服務「看起來啟動成功、但第一個請求進來才發現連不上資料庫」
// 更容易被監控與察覺。
func newPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		// 不可把 err 原文包進回傳的錯誤裡:pgx 在解析連線字串失敗時,
		// 錯誤訊息裡可能包含連線字串的片段(依錯誤發生的位置而定),
		// 而連線字串本身通常含有資料庫密碼,絕不能讓這種內容外洩到
		// 啟動失敗的訊息裡。這裡刻意捨棄 err 的內容,只回傳一句固定、
		// 不含任何動態內容的通用訊息。
		return nil, fmt.Errorf("環境變數 DATABASE_URL 的值格式不正確")
	}
	pcfg.MaxConns = cfg.DBPoolMax
	pcfg.ConnConfig.ConnectTimeout = cfg.DBConnectTimeout
	// RuntimeParams 是連線建立後,會在 PostgreSQL 連線 session 一開始
	// 就執行的 SET 參數;這裡設定 statement_timeout(單位是毫秒的字串
	// 形式,PostgreSQL 這個參數接受的格式),讓連線池裡的每一條連線都
	// 自動套用這個逾時限制,不需要每次查詢都額外下一次 SET 指令。
	pcfg.ConnConfig.RuntimeParams["statement_timeout"] =
		strconv.FormatInt(cfg.DBStatementTimeout.Milliseconds(), 10)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("建立資料庫連線池失敗")
	}

	// pgxpool.NewWithConfig 本身不會立刻嘗試連線(它是惰性的:真正需要
	// 用連線時才會建立),所以這裡明確呼叫 Ping 強制建立一次連線並確認
	// 連得通,才能在啟動階段就發現「連線字串格式正確,但資料庫主機
	// 連不上/帳密錯誤」這種問題。context.WithTimeout 避免 Ping 在
	// 資料庫真的連不上時無限期卡住,超過 DBConnectTimeout 就放棄並回報
	// 錯誤。
	pingCtx, cancel := context.WithTimeout(ctx, cfg.DBConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		// Ping 失敗時,先把已經建立的連線池關閉(pool.Close()),避免
		// 這個明知道連不上資料庫的連線池仍然被留在記憶體裡佔用資源,
		// 才回傳錯誤讓上層知道啟動失敗。
		pool.Close()
		return nil, fmt.Errorf("無法連線到資料庫,請確認 DATABASE_URL 與資料庫狀態")
	}
	return pool, nil
}
