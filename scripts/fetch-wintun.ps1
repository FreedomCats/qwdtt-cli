<#
.SYNOPSIS
    Скачивает wintun.dll фиксированной версии, проверяет её и кладёт рядом с qwdtt-cli.exe.

.DESCRIPTION
    wintun.dll нужна только режиму -datapath wintun (свой адаптер). В репозитории
    её нет: это чужой бинарь под собственной лицензией WireGuard LLC, а не наш код.
    Скрипт — единственный источник DLL и для сборки из исходников, и для
    scripts/build-release.ps1.

    Что делается:

      1. архив wintun-<версия>.zip берётся с https://www.wintun.net/builds/
         (или из -Archive, если он уже скачан);
      2. SHA256 архива сверяется с зашитым в скрипт; не совпал — скрипт падает
         и ничего не распаковывает;
      3. из архива берётся bin/<arch>/wintun.dll, и на Windows проверяется её
         подпись Authenticode: действительная, подписант — WireGuard LLC;
      4. DLL и лицензия Wintun (как wintun-LICENSE.txt) копируются в -Dest.

    Лицензия Wintun разрешает распространять DLL без изменений вместе с
    программой, которая обращается к ней только через API wintun.h, — так её
    и использует wireguard-go. Поэтому лицензия кладётся рядом с DLL.

    Сменить версию Wintun — значит поменять $WintunVersion и $ArchiveSha256
    ниже, сверив хеш с архивом, скачанным вручную с wintun.net.

.PARAMETER Arch
    Архитектура в терминах Go: amd64 (по умолчанию), arm64, 386, arm.

.PARAMETER Dest
    Куда положить wintun.dll. По умолчанию build/out в корне репозитория —
    туда же README предлагает собирать qwdtt-cli.exe.

.PARAMETER Archive
    Уже скачанный wintun-<версия>.zip: тогда в сеть скрипт не ходит. Хеш
    проверяется всё равно.

.EXAMPLE
    pwsh scripts/fetch-wintun.ps1
    pwsh scripts/fetch-wintun.ps1 -Arch arm64 -Dest C:\tools\qwdtt-cli
    pwsh scripts/fetch-wintun.ps1 -Archive $HOME\Downloads\wintun-0.14.1.zip
#>
[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64', '386', 'arm')]
    [string] $Arch = 'amd64',
    [string] $Dest,
    [string] $Archive
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

# Версия и хеш архива. Хеш — от архива с wintun.net/builds
# (plan-e2.md, врезка W2).
$WintunVersion = '0.14.1'
$ArchiveSha256 = '07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51'
$ArchiveUrl = "https://www.wintun.net/builds/wintun-$WintunVersion.zip"

# Подписант DLL. Проверяется организация, а не отпечаток сертификата:
# сертификаты продлеваются, организация остаётся.
$ExpectedSigner = 'O=WireGuard LLC'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $Dest) { $Dest = Join-Path $root 'build/out' }

# Каталоги внутри архива называются по-виндовому, а не по-гошному.
$archDir = @{ amd64 = 'amd64'; arm64 = 'arm64'; '386' = 'x86'; arm = 'arm' }[$Arch]

# Всё скачанное и распакованное живёт в своём пустом временном каталоге и
# удаляется в конце, что бы ни случилось.
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("qwdtt-cli-wintun-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    if ($Archive) {
        $zip = (Resolve-Path $Archive).Path
        Write-Host "Архив: $zip"
    }
    else {
        $zip = Join-Path $work "wintun-$WintunVersion.zip"
        Write-Host "Скачиваю $ArchiveUrl"
        Invoke-WebRequest -Uri $ArchiveUrl -OutFile $zip -UseBasicParsing
    }

    $actual = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLowerInvariant()
    if ($actual -ne $ArchiveSha256) {
        throw "SHA256 архива не совпадает: ждали $ArchiveSha256, получили $actual. " +
            "Архив подменён или это другая версия — ничего не распаковано."
    }
    Write-Host "SHA256 архива совпал: $actual"

    $unpacked = Join-Path $work 'unpacked'
    Expand-Archive -Path $zip -DestinationPath $unpacked
    $dll = Join-Path $unpacked "wintun/bin/$archDir/wintun.dll"
    $license = Join-Path $unpacked 'wintun/LICENSE.txt'
    foreach ($file in @($dll, $license)) {
        if (-not (Test-Path $file)) { throw "в архиве нет $($file.Substring($unpacked.Length + 1))" }
    }

    if ($IsWindows) {
        $sig = Get-AuthenticodeSignature -FilePath $dll
        if ($sig.Status -ne 'Valid') {
            throw "подпись wintun.dll недействительна: $($sig.Status) — $($sig.StatusMessage)"
        }
        if ($sig.SignerCertificate.Subject -notmatch [regex]::Escape($ExpectedSigner)) {
            throw "wintun.dll подписана не WireGuard LLC: $($sig.SignerCertificate.Subject)"
        }
        Write-Host "Подпись wintun.dll действительна: $ExpectedSigner"
    }
    else {
        Write-Host 'Подпись не проверялась: Get-AuthenticodeSignature есть только на Windows (хеш архива проверен).'
    }

    New-Item -ItemType Directory -Force -Path $Dest | Out-Null
    Copy-Item -Force $dll (Join-Path $Dest 'wintun.dll')
    Copy-Item -Force $license (Join-Path $Dest 'wintun-LICENSE.txt')
    Write-Host "Wintun $WintunVersion ($Arch) → $(Join-Path $Dest 'wintun.dll')"
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
