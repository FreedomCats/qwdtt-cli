<#
.SYNOPSIS
    Собирает релиз tt: три бинаря, zip для Windows и tar.gz для Linux и macOS.

.DESCRIPTION
    Сборка воспроизводима из чистого клона: скрипту нужны go, git и доступ
    к wintun.net (или уже скачанный архив Wintun, см. -WintunArchive).
    Для каждой платформы из $Platforms делается

        go build -trimpath -ldflags "-s -w -X main.version=… -X main.commit=…"

    с CGO_ENABLED=0 — cgo в проекте не нужен, а без него кросс-сборка не ищет
    чужой компилятор. Версия и коммит попадают в `tt version` через переменные
    cmd/tt/main.go.

    Раскладка результата (по умолчанию dist/):

        dist/tt_<версия>_windows_amd64/tt.exe
        dist/tt_<версия>_linux_amd64/tt
        dist/tt_<версия>_darwin_arm64/tt
        dist/tt_<версия>_windows_amd64.zip   — каталог tt_<версия>_windows_amd64/
                                               с tt.exe, wintun.dll, wintun-LICENSE.txt,
                                               THIRD_PARTY_NOTICES.txt,
                                               tt.example.toml, LICENSE, README.md
        dist/tt_<версия>_linux_amd64.tar.gz  — каталог tt_<версия>_linux_amd64/ с tt,
        dist/tt_<версия>_darwin_arm64.tar.gz   THIRD_PARTY_NOTICES.txt,
                                               tt.example.toml, LICENSE, README.md

    wintun.dll (режим -datapath tun) кладётся в zip скриптом
    scripts/fetch-wintun.ps1: фиксированная версия, проверка SHA256 архива и
    подписи DLL. В репозитории её нет. THIRD_PARTY_NOTICES.txt — лицензии
    всего, что слинковано в tt.exe, — собирает `go run ./scripts/notices`
    на каждую сборку.

    Внутри и zip, и tar.gz файлы лежат в каталоге с именем архива: распакованное
    не смешается с соседними файлами. В tar.gz у tt стоит право
    на исполнение (0755), у остальных файлов — 0644, на какой бы системе ни шла
    сборка: архив пишется средствами .NET (System.Formats.Tar), а не tar
    системы, — tar на Windows права на исполнение не ставит. Нужен pwsh 7.3
    или новее.

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
    Не собирать архивы — ни zip, ни tar.gz (и не брать wintun.dll).

.PARAMETER WintunArchive
    Уже скачанный wintun-<версия>.zip для fetch-wintun.ps1 — сборка без сети.

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
    [switch]   $NoZip,
    [string]   $WintunArchive
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
        GOARCH   = $goarch
        Dir      = $dir
        Name     = $name
        Exe      = $target
    }
}

# --- zip для Windows -------------------------------------------------------

$archives = @()
if (-not $NoZip) {
    foreach ($b in $built | Where-Object GOOS -eq 'windows') {
        foreach ($extra in @('tt.example.toml', 'LICENSE', 'README.md')) {
            Copy-Item (Join-Path $root $extra) (Join-Path $b.Dir $extra)
        }
        $fetch = @{ Arch = $b.GOARCH; Dest = $b.Dir }
        if ($WintunArchive) { $fetch.Archive = $WintunArchive }
        & (Join-Path $PSScriptRoot 'fetch-wintun.ps1') @fetch
        & go -C $root run ./scripts/notices -dest $b.Dir -goos $b.GOOS -goarch $b.GOARCH
        if ($LASTEXITCODE -ne 0) { throw "уведомления для $($b.Platform) не собраны" }
        $archive = Join-Path $Out "$($b.Name).zip"
        if (Test-Path $archive) { Remove-Item -Force $archive }
        # Внутри архива — каталог с именем архива, как в tar.gz: «Извлечь всё»
        # не рассыплет файлы по Загрузкам, а tt.exe не потеряет wintun.dll.
        Compress-Archive -Path $b.Dir -DestinationPath $archive -CompressionLevel Optimal
        $archives += $archive
        Write-Host "Архив: $archive"
    }
}

# --- tar.gz для Linux и macOS ---------------------------------------------

# New-TarGz пишет каталог $Dir в $Path: внутри — каталог $Prefix с файлами
# $Dir (вложенных каталогов в релизе нет). $Executables получают 0755.
# Время у всех записей одно и фиксированное: время файлов на машине сборки
# в архив не попадает, и одинаковые файлы дают одинаковый архив. Формат —
# ustar: pax дописывает каждой записи atime и ctime текущим временем.
function New-TarGz {
    param([string] $Dir, [string] $Path, [string] $Prefix, [string[]] $Executables)

    if ($PSVersionTable.PSVersion -lt [version]'7.3') {
        throw "tar.gz пишется через System.Formats.Tar: нужен pwsh 7.3 или новее, а это $($PSVersionTable.PSVersion)"
    }
    $exec = [System.IO.UnixFileMode]'UserRead, UserWrite, UserExecute, GroupRead, GroupExecute, OtherRead, OtherExecute'
    $plain = [System.IO.UnixFileMode]'UserRead, UserWrite, GroupRead, OtherRead'
    $mtime = [DateTimeOffset]::new(2026, 1, 1, 0, 0, 0, [TimeSpan]::Zero)

    $file = [System.IO.File]::Create($Path)
    try {
        $gzip = [System.IO.Compression.GZipStream]::new($file, [System.IO.Compression.CompressionLevel]::Optimal)
        try {
            $tar = [System.Formats.Tar.TarWriter]::new($gzip, [System.Formats.Tar.TarEntryFormat]::Ustar, $false)
            try {
                $dirEntry = [System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]::Directory, "$Prefix/")
                $dirEntry.Mode = $exec
                $dirEntry.ModificationTime = $mtime
                $tar.WriteEntry($dirEntry)
                foreach ($f in Get-ChildItem -File -LiteralPath $Dir | Sort-Object Name) {
                    $entry = [System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]::RegularFile, "$Prefix/$($f.Name)")
                    $entry.Mode = if ($Executables -contains $f.Name) { $exec } else { $plain }
                    $entry.ModificationTime = $mtime
                    $data = [System.IO.File]::OpenRead($f.FullName)
                    try {
                        $entry.DataStream = $data
                        $tar.WriteEntry($entry)
                    }
                    finally { $data.Dispose() }
                }
            }
            finally { $tar.Dispose() }
        }
        finally { $gzip.Dispose() }
    }
    finally { $file.Dispose() }
}

if (-not $NoZip) {
    foreach ($b in $built | Where-Object GOOS -ne 'windows') {
        foreach ($extra in @('tt.example.toml', 'LICENSE', 'README.md')) {
            Copy-Item (Join-Path $root $extra) (Join-Path $b.Dir $extra)
        }
        & go -C $root run ./scripts/notices -dest $b.Dir -goos $b.GOOS -goarch $b.GOARCH
        if ($LASTEXITCODE -ne 0) { throw "уведомления для $($b.Platform) не собраны" }
        $tgz = Join-Path $Out "$($b.Name).tar.gz"
        if (Test-Path $tgz) { Remove-Item -Force $tgz }
        New-TarGz -Dir $b.Dir -Path $tgz -Prefix $b.Name -Executables @('tt')
        $archives += $tgz
        Write-Host "Архив: $tgz"
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
foreach ($archive in $archives) {
    $size = [math]::Round((Get-Item $archive).Length / 1MB, 1)
    $kind = if ($archive.EndsWith('.zip')) { 'zip' } else { 'tar.gz' }
    Write-Host ("  {0,-14} {1}  ({2} МБ)" -f $kind, $archive, $size)
}
