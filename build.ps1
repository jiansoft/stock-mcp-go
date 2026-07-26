# 交叉編譯 stock-mcp:
#   - windows_amd64  → 本機開發測試用
#   - linux_arm64    → 部署裝置(對應 Dockerfile_live 的 linux/arm64)
#   - linux_armv7    → 部署裝置(對應 Dockerfile_live 的 linux/arm/v7)
#
# 本專案為純 Go、無 cgo,固定關閉 CGO 以產出靜態連結的 binary,
# 可直接放進 distroless static image 執行。
# 產物一律輸出到 bin\(已列入 .gitignore),Dockerfile_live 會從 bin\ 取用。

$ErrorActionPreference = 'Stop'

go version

New-Item -ItemType Directory -Force bin | Out-Null
Remove-Item bin\stock-mcp_* -Force -ErrorAction SilentlyContinue

$env:CGO_ENABLED = '0'

function Build-Target {
    param(
        [string]$Goos,
        [string]$Goarch,
        [string]$Goarm,
        [string]$Output
    )
    $env:GOOS = $Goos
    $env:GOARCH = $Goarch
    $env:GOARM = $Goarm
    go build -trimpath -ldflags '-s -w' -o "bin\$Output" .
    if ($LASTEXITCODE -ne 0) {
        Write-Error ">>> build $Output 失敗"
        exit 1
    }
    Write-Host ">>> build $Output done"
}

Build-Target -Goos 'windows' -Goarch 'amd64' -Goarm '' -Output 'stock-mcp_windows_amd64.exe'
Build-Target -Goos 'linux'   -Goarch 'arm64' -Goarm '' -Output 'stock-mcp_linux_arm64'
Build-Target -Goos 'linux'   -Goarch 'arm'   -Goarm '7' -Output 'stock-mcp_linux_armv7'

# 還原環境變數,避免影響同一個 shell 之後的 go 指令
$env:GOOS = ''
$env:GOARCH = ''
$env:GOARM = ''
$env:CGO_ENABLED = ''

Write-Host '>>> 全部建置完成,產物位於 bin\'
