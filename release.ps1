# 发布脚本：把当前版本推到 GitHub Release（创建 / 更新 Release、上传 FPK、生成说明）
#
# 用法：
#   .\release.ps1                     # 按 fpk\manifest 的 version 发布（需已有构建产物）
#   .\release.ps1 -Push               # 先推送当前分支，再发 / 更新 Release（推荐）
#   .\release.ps1 -Draft              # 先发草稿
#   .\release.ps1 -NoUpload           # 只更新 Release 说明，不传附件
#   .\release.ps1 -Proxy ''           # 不使用代理直连 GitHub
#
# 依赖：git（读取已保存的 GitHub 凭据）、PowerShell 5.1、已构建好的 FPK。

[CmdletBinding()]
param(
    [string]$Repo = 'IMGZCQ/AT-Field',
    [string]$Proxy = $(if ($env:ATFF_RELEASE_PROXY) { $env:ATFF_RELEASE_PROXY } else { 'http://127.0.0.1:7897' }),
    [string]$Token = '',
    [switch]$Draft,
    [switch]$Prerelease,
    [switch]$NoUpload,
    [switch]$Push
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

function Write-Step($msg) { Write-Host "[Release] $msg" -ForegroundColor Cyan }
function Write-Ok($msg) { Write-Host "[OK] $msg" -ForegroundColor Green }

# ---------- 1. 读取版本与更新说明 ----------
$manifest = Join-Path $root 'fpk\manifest'
if (-not (Test-Path $manifest)) { throw "找不到 fpk/manifest" }

$version = ''
$changelog = ''
foreach ($line in Get-Content -LiteralPath $manifest -Encoding UTF8) {
    if ($line -match '^version=(.+)$') { $version = $Matches[1].Trim() }
    elseif ($line -match '^changelog=(.*)$') { $changelog = $Matches[1] }
}
if (-not $version) { throw "fpk/manifest 里没有 version=" }
Write-Step "版本 $version"

# changelog 形如：0.1.70<br>• 内容<br>• 内容<br>0.1.69<br>...
$blocks = [regex]::Split($changelog, '<br>\s*')
$notes = New-Object System.Collections.Generic.List[string]
$capture = $false
foreach ($raw in $blocks) {
    $text = $raw.Trim().Trim('`').Trim()
    if (-not $text) { continue }
    if ($text -match '^\d+\.\d+\.\d+$') {
        if ($capture) { break }                       # 下个版本开始，结束
        if ($text -eq $version) { $capture = $true }  # 命中当前版本
        continue
    }
    if ($capture) { $notes.Add(($text -replace '^•\s*', '')) }
}
if ($notes.Count -eq 0) { Write-Host "[Warn] manifest 里没有 $version 的更新条目" -ForegroundColor Yellow }

$bullets = ($notes | ForEach-Object { "- $_" }) -join "`r`n"

# ---------- 2. 找构建产物 ----------
$fpk = Join-Path $root ("飞牛AT力场_{0}.fpk" -f $version)
if (-not $NoUpload -and -not (Test-Path -LiteralPath $fpk)) {
    throw ("找不到构建产物 {0}，请先运行 build.bat" -f (Split-Path -Leaf $fpk))
}
$assetName = "AT-ForceField_$version.fpk"   # 附件用纯 ASCII 名，避免下载方 / 脚本出错
$sha = ''
if (Test-Path -LiteralPath $fpk) {
    $sha = (Get-FileHash -LiteralPath $fpk -Algorithm SHA256).Hash.ToLower()
}

$tag = "v$version"
$releaseName = "飞牛AT力场 $version"

$body = @"
## 飞牛AT力场 $version

$bullets

### 安装 / 升级

- 下载下方 ``$assetName``，在飞牛应用中心「手动安装」选择该文件
- 已安装旧版本可直接覆盖安装，配置、告警设置与历史数据都会保留
- 安装包 SHA256：``$sha``

> 完整更新历史见仓库 [fpk/manifest](https://github.com/$Repo/blob/main/fpk/manifest)。
"@

# ---------- 3. 推送分支（可选） ----------
$gitProxyArgs = @()
if ($Proxy) { $gitProxyArgs = @('-c', "http.proxy=$Proxy", '-c', "https.proxy=$Proxy") }

if ($Push) {
    Write-Step '推送当前分支到 origin'
    & git @gitProxyArgs push origin HEAD
    if ($LASTEXITCODE -ne 0) { throw 'git push 失败，Release 未创建' }
    Write-Ok '分支已推送'
} else {
    try {
        $local = (& git rev-parse HEAD).Trim()
        $remote = ((& git @gitProxyArgs ls-remote origin refs/heads/main) -split '\s+')[0]
        if ($local -and $remote -and $local -ne $remote) {
            Write-Host '[Warn] 本地提交尚未推送，Release 标签会落在远端旧提交上；建议先 git push 或加 -Push' -ForegroundColor Yellow
        }
    } catch {
        Write-Host '[Warn] 无法比对远端提交（网络 / 代理），跳过检查' -ForegroundColor Yellow
    }
}

# ---------- 4. 取 GitHub Token ----------
if (-not $Token) {
    Write-Step '读取已保存的 GitHub 凭据（git credential）'
    $env:GIT_TERMINAL_PROMPT = '0'
    # 用临时文件做标准输入，避免管道末尾空行被吞导致 git 拿不到 protocol 字段
    $base = Join-Path $env:TEMP ('atff-cred-' + [guid]::NewGuid().ToString('N'))
    $inF = "$base.in"; $outF = "$base.out"; $errF = "$base.err"
    [IO.File]::WriteAllText($inF, "protocol=https`r`nhost=github.com`r`n`r`n")
    Start-Process -FilePath 'git' -ArgumentList 'credential', 'fill' `
        -RedirectStandardInput $inF -RedirectStandardOutput $outF -RedirectStandardError $errF `
        -NoNewWindow -Wait | Out-Null
    $credText = ''
    if (Test-Path -LiteralPath $outF) { $credText = [IO.File]::ReadAllText($outF) }
    Remove-Item -LiteralPath $inF, $outF, $errF -ErrorAction SilentlyContinue
    foreach ($line in ($credText -split "`r?`n")) {
        if ($line -match '^password=(.+)$') { $Token = $Matches[1].Trim() }
    }
    if (-not $Token) { throw '没有取到 GitHub Token，请先执行 git push 登录一次，或用 -Token 传入' }
}

$headers = @{
    Authorization          = "token $Token"
    'User-Agent'           = 'AT-ForceField-Release'
    Accept                 = 'application/vnd.github+json'
}
$apiBase = "https://api.github.com/repos/$Repo"

# 依次尝试：代理 -> 直连（代理不可用时自动回退）
$proxyCandidates = @()
if ($Proxy) { $proxyCandidates += $Proxy }
$proxyCandidates += ''

function Invoke-GH {
    param([string]$Method, [string]$Uri, $Body, [string]$ContentType = 'application/json; charset=utf-8', [int]$TimeoutSec = 120)
    $lastErr = $null
    foreach ($p in $proxyCandidates) {
        try {
            $params = @{ Method = $Method; Uri = $Uri; Headers = $headers; TimeoutSec = $TimeoutSec }
            if ($p) { $params['Proxy'] = $p }
            if ($Body -ne $null) {
                $params['ContentType'] = $ContentType
                $params['Body'] = $Body
            }
            return Invoke-RestMethod @params
        } catch {
            $lastErr = $_
        }
    }
    throw $lastErr
}

Write-Step "校验仓库与权限：$Repo"
$repoInfo = Invoke-GH -Method Get -Uri $apiBase
Write-Ok ("仓库可达：{0}（默认分支 {1}）" -f $repoInfo.full_name, $repoInfo.default_branch)

# ---------- 5. 创建或更新 Release ----------
$release = $null
try {
    $release = Invoke-GH -Method Get -Uri "$apiBase/releases/tags/$tag"
    Write-Step "Release $tag 已存在，改为更新说明"
} catch {
    Write-Step "创建 Release $tag"
}

$payload = @{
    tag_name         = $tag
    target_commitish = 'main'
    name             = $releaseName
    body             = $body
    draft            = [bool]$Draft
    prerelease       = [bool]$Prerelease
} | ConvertTo-Json -Depth 4

if ($release) {
    $release = Invoke-GH -Method Patch -Uri "$apiBase/releases/$($release.id)" -Body ([Text.Encoding]::UTF8.GetBytes($payload))
} else {
    $release = Invoke-GH -Method Post -Uri "$apiBase/releases" -Body ([Text.Encoding]::UTF8.GetBytes($payload))
}
Write-Ok ("Release 就绪：{0}" -f $release.html_url)

# ---------- 6. 上传附件 ----------
if (-not $NoUpload) {
    $existing = @()
    try { $existing = Invoke-GH -Method Get -Uri "$apiBase/releases/$($release.id)/assets" } catch { }
    $same = $existing | Where-Object { $_.name -eq $assetName }
    foreach ($a in $same) {
        Write-Step "删除同名旧附件 $assetName"
        Invoke-GH -Method Delete -Uri "$apiBase/releases/assets/$($a.id)" | Out-Null
    }

    Write-Step "上传 $assetName（$([math]::Round((Get-Item -LiteralPath $fpk).Length / 1MB, 2)) MB）"
    $uploadUrl = "https://uploads.github.com/repos/$Repo/releases/$($release.id)/assets?name=$assetName"
    $uploadParams = @{
        Method      = 'Post'
        Uri         = $uploadUrl
        Headers     = $headers
        ContentType = 'application/octet-stream'
        InFile      = $fpk
        TimeoutSec  = 600
    }
    $uploaded = $null
    foreach ($p in $proxyCandidates) {
        try {
            $pp = $uploadParams.Clone()
            if ($p) { $pp['Proxy'] = $p }
            $uploaded = Invoke-RestMethod @pp
            break
        } catch { $uploaded = $null }
    }
    if (-not $uploaded) { throw '附件上传失败' }
    Write-Ok ("附件已上传：{0}（{1} 字节）" -f $uploaded.name, $uploaded.size)
}

Write-Host ''
Write-Ok "完成：$($release.html_url)"
