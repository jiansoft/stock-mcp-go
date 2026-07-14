# 常用開發指令(對應規格書要求的 scripts)

.PHONY: dev build start test test-watch lint fmt fmt-check

dev: ## 以本機 .env 啟動開發伺服器
	go run .

build: ## 編譯執行檔
	go build -trimpath -o stock-mcp .

start: build ## 編譯後啟動
	./stock-mcp

test: ## 執行全部測試
	go test ./...

test-watch: ## 監看模式(需安裝 gotestsum:go install gotest.tools/gotestsum@latest)
	gotestsum --watch

lint: ## 靜態檢查
	go vet ./...

fmt: ## 格式化
	gofmt -w .

fmt-check: ## 檢查格式(有未格式化檔案時失敗)
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
