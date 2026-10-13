<#
.SYNOPSIS
    Собирает релиз tt: три бинаря и zip для Windows.

.DESCRIPTION
    Сборка воспроизводима из чистого клона: скрипту нужны только go и git.
    Для каждой платформы из $Platforms делается

        go build -trimpath -ldflags "-s -w -X main.version=… -X main.commit=…"

    с CGO_ENABLED=0 — cgo в проекте не нужен, а без него кросс-сборка не ищет
    чужой компилятор. Версия и коммит попадают в `tt version` через переменные
    cmd/tt/main.go.

    Раскладка результата (по умолчанию dist/):

        dist/tt_<версия>_windows_amd64/tt.exe
        dist/tt_<версия>_linux_amd64/tt
        dist/tt_<версия>_darwin_arm64/tt
        dist/tt_<версия>_windows_amd64.zip   — tt.exe, tt.example.toml, LICENSE, README.md

    Zip собирается только для Windows: это единственная платформа, для которой
    в Э1 есть все datapath и живая проверка. Остальные бинари лежат рядом как есть.

    В конце скрипт запускает `tt version` у бинаря для своей платформы и
    проверяет, что версия и коммит подставились.

.PARAMETER Version
    Строка версии. По умолчанию — `git describe --tags --always --dirty`,
    то есть тег, если он есть, иначе короткий коммит; суффикс -dirty при
    незакоммиченных изменениях.

.PARAMETER Out
    Каталог результата. По умолчанию dist в корне репозитория. Содержимое
    для собираемых платформ перезаписывается; чужие файлы не трогаются.

.PARAMETER Platforms
    Список платформ в виде os/arch. По умолчанию windows/amd64, linux/amd64,
    darwin/arm64.

.PARAMETER NoZip
    Не собирать zip.

.EXAMPLE
    pwsh scripts/build-release.ps1
    pwsh scripts/build-release.ps1 -Version 0.1.0
    pwsh scripts/build-release.ps1 -Platforms windows/amd64 -Out C:\tmp\tt
#>
[CmdletBinding()]
param(
    [string]   $Version,
    [string]   $Out,
    [string[]] $Platforms = @('windows/amd64', 'linux/amd64', 'darwin/arm64'),
    [switch]   $NoZip
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# Сообщения по-русски: в консоли с кодовой страницей OEM они иначе превращаются
# в вопросительные знаки. Сам tt пишет UTF-8 всегда.
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $Out) { $Out = Join-Path $root 'dist' }

# --- сведения о сборке -----------------------------------------------------

function Invoke-Git {
    param([string[]] $GitArgs)
    $result = & git -C $root @GitArgs 2>$null
    if ($LASTEXITCODE -ne 0) { return $null }
    return ($result | Out-String).Trim()
}

if (-not $Version) {
    $Version = Invoke-Git @('describe', '--tags', '--always', '--dirty')
    if (-not $Version) { $Version = 'dev' }
}
# Версия попадает в имена каталогов и файлов: слэши и пробелы там не нужны.
$Version = $Version -replace '[\\/\s]', '-'

$commit = Invoke-Git @('rev-parse', '--short', 'HEAD')
if (-not $commit) { $commit = 'unknown' }
elseif (Invoke-Git @('status', '--porcelain')) { $commit += '-dirty' }

$ldflags = "-s -w -X main.version=$Version -X main.commit=$commit"

Write-Host "Версия: $Version, коммит: $commit"
Write-Host "Каталог: $Out"

# --- сборка ----------------------------------------------------------------

New-Item -ItemType Directory -Force -Path $Out | Out-Null

$built = @()
foreach ($platform in $Platforms) {
    $parts = $platform -split '/'
    if ($parts.Count -ne 2) { throw "платформа должна быть в виде os/arch: '$platform'" }
    $goos, $goarch = $parts

    $name = "tt_${Version}_${goos}_${goarch}"
    $dir = Join-Path $Out $name
    $exe = if ($goos -eq 'windows') { 'tt.exe' } else { 'tt' }
    $target = Join-Path $dir $exe

    if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null

    Write-Host "Сборка $goos/$goarch → $target"
    $env:GOOS = $goos
    $env:GOARCH = $goarch
    $env:CGO_ENABLED = '0'
    try {
        & go build -trimpath -ldflags $ldflags -o $target ./cmd/tt
        if ($LASTEXITCODE -ne 0) { throw "go build для $platform завершился с кодом $LASTEXITCODE" }
    }
    finally {
        Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    }

    $built += [pscustomobject]@{
        Platform = $platform
        GOOS     = $goos
        Dir      = $dir
        Name     = $name
        Exe      = $target
    }
}

# --- zip для Windows -------------------------------------------------------

$zips = @()
if (-not $NoZip) {
    foreach ($b in $built | Where-Object GOOS -eq 'windows') {
        foreach ($extra in @('tt.example.toml', 'LICENSE', 'README.md')) {
            Copy-Item (Join-Path $root $extra) (Join-Path $b.Dir $extra)
        }
        $zip = Join-Path $Out "$($b.Name).zip"
        if (Test-Path $zip) { Remove-Item -Force $zip }
        # Внутри архива — файлы без каталога: распаковал и запустил.
        Compress-Archive -Path (Join-Path $b.Dir '*') -DestinationPath $zip -CompressionLevel Optimal
        $zips += $zip
        Write-Host "Архив: $zip"
    }
}

# --- проверка --------------------------------------------------------------

$hostOS = if ($IsWindows) { 'windows' } elseif ($IsLinux) { 'linux' } elseif ($IsMacOS) { 'darwin' } else { '' }
$hostArch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { '' }
}
$native = $built | Where-Object Platform -eq "$hostOS/$hostArch" | Select-Object -First 1
if ($native) {
    $versionOut = (& $native.Exe version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw "tt version завершился с кодом $LASTEXITCODE" }
    if ($versionOut -notmatch [regex]::Escape($Version) -or $versionOut -notmatch [regex]::Escape($commit)) {
        throw "tt version не показывает версию и коммит: '$versionOut'"
    }
    Write-Host "Проверка: $versionOut"
}
else {
    Write-Host "Проверка tt version пропущена: бинаря для $hostOS/$hostArch среди собранных нет"
}

# --- итог ------------------------------------------------------------------

Write-Host ''
Write-Host 'Готово:'
foreach ($b in $built) {
    $size = [math]::Round((Get-Item $b.Exe).Length / 1MB, 1)
    Write-Host ("  {0,-14} {1}  ({2} МБ)" -f $b.Platform, $b.Exe, $size)
}
foreach ($zip in $zips) {
    $size = [math]::Round((Get-Item $zip).Length / 1MB, 1)
    Write-Host ("  {0,-14} {1}  ({2} МБ)" -f 'zip', $zip, $size)
}
