<#
.SYNOPSIS
    把 stock-mcp 的 linux/arm v7 執行檔部署到 Raspberry Pi（原生程序，非 Docker）。

.DESCRIPTION
    流程：
      1. （-Build）交叉編譯 bin\stock-mcp_linux_armv7（CGO 關閉、靜態連結）。
      2. 本機預檢：確認是 32 位元 ARM ELF，計算 sha256。
      3. 遠端預檢：架構是 armv7l、部署目錄有 control.sh。
      4. scp 到 /tmp，比對遠端 sha256（不直接覆蓋執行中的檔案，避免 Text file busy）。
      5. ssh 執行 control.sh update（stop → 備份舊檔並搬移 → start）。
      6. 驗證：程序存在、埠在監聽、/healthz 與 /readyz 回 200、stdout 有啟動訊息。

    驗證段落以 here-string 組成遠端 shell 指令。檔案在 Windows 上可能是 CRLF，
    送出前一律去掉 \r，否則 bash 會把 `set -` 之類的指令讀成 `set -\r` 而報錯。

.PARAMETER Target
    SSH 目標，預設 pi@192.168.111.138。

.PARAMETER IdentityFile
    SSH 私鑰路徑，預設 $env:USERPROFILE\.ssh\138.key。

.PARAMETER SshPort
    SSH 連接埠，預設 22。

.PARAMETER Binary
    要部署的執行檔；省略時用 bin\stock-mcp_linux_armv7。

.PARAMETER RemoteBase
    Pi 上的部署目錄，預設 /opt/stock_mcp（control.sh、.env、data\ 都在這裡）。

.PARAMETER Port
    服務埠，預設 9005（與 Pi 上 .env 的 PORT 一致）。

.PARAMETER Build
    先交叉編譯 armv7 執行檔再部署。

.PARAMETER StageOnly
    只上傳到 /tmp 並比對 sha256，不重啟服務。

.PARAMETER SkipVerify
    部署後不做驗證。

.EXAMPLE
    .\scripts\deploy-armv7.ps1 -Build

.EXAMPLE
    .\scripts\deploy-armv7.ps1 -StageOnly
#>
[CmdletBinding()]
param(
    [string]$Target = 'pi@192.168.111.138',
    [string]$IdentityFile = "$env:USERPROFILE\.ssh\138.key",
    [int]$SshPort = 22,
    [string]$Binary,
    [string]$RemoteBase = '/opt/stock_mcp',
    [int]$Port = 9005,
    [switch]$Build,
    [switch]$StageOnly,
    [switch]$SkipVerify
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$RepoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$BinaryName = 'stock-mcp_linux_armv7'

function Invoke-Checked
{
    param(
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
    )

    & $Command @Arguments
    if ($LASTEXITCODE -ne 0)
    {
        throw "指令失敗（exit $LASTEXITCODE）：$Command $( $Arguments -join ' ' )"
    }
}

# 只讀檔頭：32 位元（EI_CLASS=1）、little-endian、e_machine=40（ARM）。
function Test-Arm32Elf
{
    param([Parameter(Mandatory = $true)][string]$Path)

    $head = New-Object byte[] 64
    $stream = [IO.File]::OpenRead($Path)
    try
    {
        $read = $stream.Read($head, 0, $head.Length)
    }
    finally
    {
        $stream.Dispose()
    }
    if ($read -lt 52 -or $head[0] -ne 0x7F -or $head[1] -ne 0x45 -or $head[2] -ne 0x4C -or $head[3] -ne 0x46)
    {
        throw "不是 ELF 執行檔：$Path"
    }
    if ($head[4] -ne 1 -or $head[5] -ne 1)
    {
        throw "不是 32 位元 little-endian ELF：$Path"
    }
    $machine = [BitConverter]::ToUInt16($head, 0x12)
    if ($machine -ne 40)
    {
        throw "e_machine=$machine，不是 ARM（40）：$Path"
    }
    [Math]::Round((Get-Item -LiteralPath $Path).Length / 1MB, 1)
}

# ssh/scp 共用參數：BatchMode 讓沒有金鑰時直接失敗，不會卡在互動式密碼提示。
$SshArgs = @('-p', "$SshPort", '-o', 'BatchMode=yes')
$ScpArgs = @('-P', "$SshPort", '-o', 'BatchMode=yes')
if (-not [string]::IsNullOrWhiteSpace($IdentityFile))
{
    if (-not (Test-Path -LiteralPath $IdentityFile))
    {
        throw "找不到 SSH 私鑰：$IdentityFile"
    }
    $SshArgs = @('-i', $IdentityFile) + $SshArgs
    $ScpArgs = @('-i', $IdentityFile) + $ScpArgs
}

function Invoke-Remote
{
    param([Parameter(Mandatory = $true)][string]$Script)
    & ssh @SshArgs $Target ($Script -replace "`r", '')
}

# --- 1. 建置 ----------------------------------------------------------------
if ($Build)
{
    Write-Host '交叉編譯 linux/arm v7...' -ForegroundColor Cyan
    New-Item -ItemType Directory -Force (Join-Path $RepoRoot 'bin') | Out-Null
    $saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; GOARM = $env:GOARM; CGO_ENABLED = $env:CGO_ENABLED }
    try
    {
        $env:GOOS = 'linux'; $env:GOARCH = 'arm'; $env:GOARM = '7'; $env:CGO_ENABLED = '0'
        Push-Location $RepoRoot
        try
        {
            # 直接呼叫而不經 Invoke-Checked：-o 這類旗標會被 PowerShell 當成函式自己的參數。
            & go build -trimpath -ldflags '-s -w' -o "bin/$BinaryName" .
            if ($LASTEXITCODE -ne 0)
            {
                throw "go build 失敗（exit $LASTEXITCODE）"
            }
        }
        finally
        {
            Pop-Location
        }
    }
    finally
    {
        foreach ($name in $saved.Keys)
        {
            Set-Item -Path "env:$name" -Value $saved[$name]
        }
    }
}

if ([string]::IsNullOrWhiteSpace($Binary))
{
    $Binary = Join-Path $RepoRoot "bin\$BinaryName"
}
if (-not (Test-Path -LiteralPath $Binary))
{
    throw "找不到執行檔：$Binary（先執行 build.ps1，或加 -Build）"
}
$Binary = (Resolve-Path -LiteralPath $Binary).ProviderPath

Write-Host '本機預檢...' -ForegroundColor Cyan
$sizeMB = Test-Arm32Elf -Path $Binary
$localHash = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLower()
$commit = (& git -C $RepoRoot rev-parse --short HEAD 2>$null)
Write-Host "  BINARY   $Binary ($sizeMB MB)"
Write-Host "  COMMIT   $commit"
Write-Host "  SHA256   $localHash"

# --- 2. 遠端預檢 -------------------------------------------------------------
$remoteInfo = Invoke-Remote "uname -m; test -f $RemoteBase/control.sh && echo CONTROL_OK || echo CONTROL_MISSING; test -f $RemoteBase/.env && echo ENV_OK || echo ENV_MISSING"
if ($LASTEXITCODE -ne 0)
{
    throw "SSH 連線失敗：$Target"
}
$arch = ($remoteInfo | Select-Object -First 1).Trim()
if ($arch -notin @('armv7l', 'armv7'))
{
    throw "遠端架構是 $arch，不是 armv7l"
}
foreach ($marker in 'CONTROL_OK', 'ENV_OK')
{
    if ($remoteInfo -notcontains $marker)
    {
        throw "遠端 $RemoteBase 缺少 $( $marker -replace '_OK', '' )（control.sh 或 .env）"
    }
}
Write-Host "  REMOTE   $Target ($arch, $RemoteBase)"
Write-Host ''

# --- 3. 上傳 ----------------------------------------------------------------
Write-Host '上傳執行檔...' -ForegroundColor Cyan
Invoke-Checked scp @ScpArgs $Binary "${Target}:/tmp/$BinaryName"
$remoteHash = (Invoke-Remote "sha256sum /tmp/$BinaryName | cut -d' ' -f1" | Select-Object -First 1).Trim()
if ($remoteHash -ne $localHash)
{
    throw "上傳後 sha256 不符（遠端 $remoteHash），未執行 update"
}
Write-Host "  已上傳並通過 sha256 比對：${Target}:/tmp/$BinaryName"
Write-Host ''

if ($StageOnly)
{
    Write-Host '已上傳但未部署（-StageOnly）。要上線請執行：' -ForegroundColor Yellow
    Write-Host "  ssh $Target `"cd $RemoteBase && ./control.sh update`"" -ForegroundColor Yellow
    return
}

# --- 4. 部署 ----------------------------------------------------------------
Write-Host 'control.sh update...' -ForegroundColor Cyan
Invoke-Remote "cd $RemoteBase && ./control.sh update"
if ($LASTEXITCODE -ne 0)
{
    throw 'control.sh update 失敗'
}
Write-Host ''

if ($SkipVerify)
{
    Write-Host '已略過驗證（-SkipVerify）。' -ForegroundColor Yellow
    return
}

# --- 5. 驗證 ----------------------------------------------------------------
Write-Host '驗證中...' -ForegroundColor Cyan
Start-Sleep -Seconds 3
$verify = @"
echo "--- proc ---"
pid="`$(pidof $BinaryName)"
if [ -n "`$pid" ]; then ps -p "`$pid" -o pid=,etimes=,args=; else echo NONE; fi
echo "--- listen ---"
ss -lntp 2>/dev/null | grep -E ':$Port\b' || echo NONE
echo "--- health ---"
for i in 1 2 3 4 5; do
  h="`$(curl -s -m 5 -o /dev/null -w '%{http_code}' http://127.0.0.1:$Port/healthz || echo 000)"
  r="`$(curl -s -m 10 -o /dev/null -w '%{http_code}' http://127.0.0.1:$Port/readyz || echo 000)"
  echo "attempt `$i healthz=`$h readyz=`$r"
  if [ "`$h" = "200" ] && [ "`$r" = "200" ]; then break; fi
  sleep 3
done
echo "--- startup ---"
# healthz/readyz 也會寫進 nohup.out，啟動訊息可能已被擠出 tail，改用 grep 找第一筆。
grep -m1 '已啟動' "$RemoteBase/nohup.out" 2>/dev/null || echo NONE
"@
$result = Invoke-Remote $verify
$result | ForEach-Object { Write-Host "  $_" }

$text = $result -join "`n"
$problems = @()
if ($text -notmatch [regex]::Escape($BinaryName)) { $problems += '找不到執行中的程序' }
if ($text -notmatch ":$Port") { $problems += "埠 $Port 沒有在監聽" }
if ($text -notmatch 'healthz=200 readyz=200') { $problems += '/healthz 或 /readyz 沒有回 200（readyz 失敗多半是上游 Data API 或金鑰問題）' }
if ($text -notmatch '已啟動') { $problems += 'stdout 沒有啟動訊息' }

Write-Host ''
if ($problems.Count -gt 0)
{
    Write-Host '驗證失敗：' -ForegroundColor Red
    $problems | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
    Write-Host '回滾：control.sh 會把舊檔備份成 <執行檔>.<時間戳>：' -ForegroundColor Yellow
    Write-Host "  ssh $Target `"cd $RemoteBase && ls -t $BinaryName.* | head -1`"" -ForegroundColor Yellow
    Write-Host "  ssh $Target `"cd $RemoteBase && ./control.sh stop && mv <上一行的備份檔> $BinaryName && chmod +x $BinaryName && ./control.sh start`"" -ForegroundColor Yellow
    exit 1
}
Write-Host "部署成功：${Target}:$RemoteBase/$BinaryName（$commit）" -ForegroundColor Green
