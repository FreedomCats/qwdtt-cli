<#
.SYNOPSIS
    Переносит ядро qWDTT из апстрима в internal/core.

.DESCRIPTION
    Единственный способ обновить ядро. Правки внутри апстримных файлов держатся
    механическими и живут только здесь (решение Р7 в doc/architecture.md), поэтому
    обновление сводится к запуску этого скрипта и просмотру диффа.

    Что делает:
      1. Копирует *.go из <upstream>/go_client в internal/core.
      2. Меняет package main на package core.
      3. Применяет платформенные правки, без которых ядро не собирается
         под Windows и macOS (см. раздел "правки" ниже).
      4. Записывает internal/core/CORE_VERSION с коммитом апстрима.

    Скрипт идемпотентен: повторный запуск на том же апстриме даёт тот же результат.
    Файлы, которых нет ни в апстриме, ни в списке генерируемых, не трогаются —
    так собственный код (api.go, events.go) переживает обновление ядра.

.PARAMETER Upstream
    Каталог клона proxy-turn-vk-android. По умолчанию — соседний с этим репозиторием.

.PARAMETER Check
    Ничего не писать, только сообщить, разошёлся ли internal/core с апстримом.
    Код возврата 1, если есть расхождения.

.EXAMPLE
    pwsh scripts/vendor-core.ps1
    pwsh scripts/vendor-core.ps1 -Upstream D:\src\proxy-turn-vk-android
    pwsh scripts/vendor-core.ps1 -Check
#>
[CmdletBinding()]
param(
    [string] $Upstream,
    [switch] $Check
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$target = Join-Path $repoRoot 'internal/core'

if (-not $Upstream) {
    $Upstream = Join-Path (Split-Path -Parent $repoRoot) 'proxy-turn-vk-android'
}

$source = Join-Path $Upstream 'go_client'
if (-not (Test-Path -LiteralPath $source -PathType Container)) {
    throw "Не найден каталог ядра: $source. Укажите клон апстрима через -Upstream."
}

# =============================== правки =========================================
# Всё, что меняется в апстримном коде, описано здесь и только здесь.

# tun_fd.go получает TUN-дескриптор от Android через SCM_RIGHTS и использует
# golang.org/x/sys/unix, которого нет под Windows. Ограничиваем файл Linux
# и даём заглушку остальным платформам.
$buildTags = @{
    'tun_fd.go' = '//go:build linux'
}

# listen.go задаёт SO_REUSEADDR через syscall.SetsockoptInt, а его сигнатура
# на Windows принимает syscall.Handle вместо int. Разносим по платформам.
$fileOverrides = @{
    'listen.go' = @'
package core

import (
	"context"
	"net"
)

// listenUDP binds a UDP socket with SO_REUSEADDR so a quick restart can reclaim
// 127.0.0.1:port after the previous client process exits.
func listenUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setReuseAddr}
	return lc.ListenPacket(context.Background(), "udp", addr)
}
'@
}

# Файлы, которых в апстриме нет вовсе — их создаёт этот скрипт.
$generatedFiles = @{
    'tun_fd_stub.go' = @'
//go:build !linux

package core

import (
	"fmt"
	"os"
)

// recvTunFD существует только ради компиляции: режим rawtun работает через
// передачу дескриптора по SCM_RIGHTS и доступен лишь на Linux и Android.
func recvTunFD(sockPath string) (*os.File, error) {
	return nil, fmt.Errorf("режим rawtun (передача TUN fd) доступен только на Linux/Android")
}
'@

    'listen_unix.go' = @'
//go:build !windows

package core

import "syscall"

func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}
'@

    'listen_windows.go' = @'
//go:build windows

package core

import "syscall"

// На Windows syscall.SetsockoptInt принимает syscall.Handle, а не int.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}
'@
}

# Замены в апстримном коде (задача B2): маркеры stdout и рамки с конфигом
# становятся событиями из events.go.
#
# Правила ниже — единственное место, где эти правки живут. Прямая правка
# internal/core/*.go будет затёрта при следующем переносе ядра.
#
# Форма правила:
#   File          — имя файла в go_client;
#   Count         — сколько раз шаблон обязан совпасть;
#   Pattern       — regex по тексту с LF; группа 1 обязана захватывать отступ;
#   Replace       — чем заменить совпадение целиком;
#   InsertAfter   — строка, дописываемая после совпадения с тем же отступом;
#   InsertBefore  — то же, но перед совпадением.
#
# Count проверяется: если апстрим перепишет строку и шаблон перестанет совпадать,
# скрипт упадёт. Молча потерять правку хуже, чем упасть.
$replacements = @(
    # --- шесть маркеров stdout ---
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("HASH_CHECK_START\|%d\|%s\\n", i\+1, hash\)$'
        Replace = '${1}emitHashCheck(i+1, len(hashes), hash, HashCheckRunning, "")'
    }
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("HASH_CHECK\|%d\|%s\|%s\|%s\\n", i\+1, hash, status, sanitizeHashCheckMessage\(message\)\)$'
        Replace = '${1}emitHashCheck(i+1, len(hashes), hash, HashCheckStatus(status), sanitizeHashCheckMessage(message))'
    }
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("PING_RESULT\|%d\\n", rtt\)$'
        Replace = '${1}emitPing(rtt)'
    }
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("PING_ERROR\|All hashes failed\. Last error: %v\\n", lastErr\)$'
        Replace = '${1}emitPingError(lastErr)'
    }
    @{
        File    = 'creds.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("CAPTCHA_SOLVE\|%s\|%s\|%s\\n", mode, captchaErr\.RedirectURI, captchaErr\.SessionToken\)$'
        Replace = '${1}emit(EventCaptchaRequired{Session: streamID, Mode: mode, RedirectURI: captchaErr.RedirectURI, SessionToken: captchaErr.SessionToken})'
    }
    @{
        File    = 'vk_account.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Printf\("VK_AUTH_REQUIRED\|%s\\n", link\)$'
        Replace = '${1}emit(EventVkAuthRequired{Link: link})'
    }

    # --- рамки с конфигом, которые ядро рисовало в stdout ---
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Println\(\)\n\t+fmt\.Println\("╔[^"]*RAW Конфиг[^"]*╗"\)\n(?:[^\n]*\n){3}\t+fmt\.Println\("╚[^"]*╝"\)$'
        Replace = '${1}emit(EventRawConfig{IP: ip, DNS: dnsCSV, MTU: mtuStr})'
    }
    @{
        File    = 'main.go'
        Count   = 1
        Pattern = '(?m)^(\t+)fmt\.Println\(\)\n\t+fmt\.Println\("╔[^"]*WireGuard Конфиг[^"]*╗"\)\n(?:[^\n]*\n){3}\t+fmt\.Println\("╚[^"]*╝"\)$'
        Replace = '${1}emit(EventConfig{WireGuard: finalConf})'
    }

    # --- шаги подключения ---
    # Ядро отправляет только середину схемы: DNS проверяется до его запуска,
    # а VPN/SOCKS/DONE закрывает супервизор по EventConfig (см. events.go).
    @{
        File        = 'main.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[WRAP\] Ключ выведен из пароля, режим RTP AEAD активен"\)$'
        InsertAfter = 'emitStage(StageWrap, StageOK, 0, "")'
    }
    @{
        File        = 'group.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[ГРУППА #%d\] Креды OK, TURN: %v, %d воркеров", groupID, creds\.TurnURLs, len\(workerIDs\)\)$'
        InsertAfter = 'emitStage(StageVK, StageOK, 0, "")'
    }
    @{
        File         = 'creds.go'
        Count        = 1
        Pattern      = '(?m)^(\t+)successToken, solveErr := solveCaptchaBySelectedMode\(ctx, streamID, attempt\+1, captchaErr, client, profile, savedProfile\)$'
        InsertBefore = 'emitStage(StageCaptcha, StageRunning, streamID, "")'
    }
    @{
        File        = 'creds.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[STREAM %d\] \[Captcha\] Solve failed: %v", streamID, solveErr\)$'
        InsertAfter = 'emitStage(StageCaptcha, StageFailed, streamID, solveErr.Error())'
    }
    # Единственное место, куда попадают только после успешного решения капчи:
    # дальше ядро складывает запрос с уже полученным success_token.
    @{
        File         = 'creds.go'
        Count        = 1
        Pattern      = '(?m)^(\t+)captchaAttempt := captchaErr\.CaptchaAttempt$'
        InsertBefore = 'emitStage(StageCaptcha, StageOK, streamID, "")'
    }
    @{
        File        = 'session.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[СЕССИЯ #%d\] Relay: %s", sessionID, relay\.LocalAddr\(\)\)$'
        InsertAfter = 'emitStage(StageTURN, StageOK, sessionID, relay.LocalAddr().String())'
    }
    @{
        File        = 'session.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[ПРЯМОЙ\] Без DTLS, только RTP-obfs AEAD ✓", sessionID\)$'
        InsertAfter = 'emitStage(StageDTLS, StageOK, sessionID, "без DTLS, только RTP-obfs AEAD")'
    }
    @{
        File        = 'session.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Рукопожатие \(Handshake\)\.\.\.", sessionID\)$'
        InsertAfter = 'emitStage(StageDTLS, StageRunning, sessionID, "")'
    }
    @{
        File         = 'session.go'
        Count        = 1
        Pattern      = '(?m)^(\t+)return false, fmt\.Errorf\("DTLS хендшейк: %w", err\)$'
        InsertBefore = 'emitStage(StageDTLS, StageFailed, sessionID, err.Error())'
    }
    @{
        File        = 'session.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Соединение установлено ✓", sessionID\)$'
        InsertAfter = 'emitStage(StageDTLS, StageOK, sessionID, "")'
    }
    @{
        File        = 'session.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[READY\] Туннель готов к работе ✓", sessionID\)$'
        InsertAfter = 'emitStage(StageWorkers, StageOK, sessionID, "")'
    }

    # --- счётчики трафика ---
    @{
        File        = 'stats.go'
        Count       = 1
        Pattern     = '(?m)^(\t+)log\.Printf\("\[СТАТИСТИКА\] Активных: %d \| Трафик: %\.2f МБ \| ↓%\.2f МБ / ↑%\.2f МБ", active, totalMB, downMB, upMB\)$'
        InsertAfter = 'emit(EventStats{ActiveWorkers: active, BytesUp: up, BytesDown: down})'
    }
)

# ================================================================================

function Get-UpstreamVersion {
    param([string] $Path)

    if (-not (Test-Path -LiteralPath (Join-Path $Path '.git'))) {
        return [pscustomobject]@{ Commit = 'unknown'; Date = 'unknown'; Dirty = $false }
    }

    Push-Location -LiteralPath $Path
    try {
        $commit = (& git rev-parse HEAD).Trim()
        $date = (& git log -1 --format=%cI).Trim()
        $dirty = [bool] (& git status --porcelain -- go_client)
        return [pscustomobject]@{ Commit = $commit; Date = $date; Dirty = $dirty }
    } finally {
        Pop-Location
    }
}

function Convert-CoreFile {
    param(
        [string] $Name,
        [string] $Text
    )

    if ($fileOverrides.ContainsKey($Name)) {
        return $fileOverrides[$Name]
    }

    # Шаблоны замен написаны под LF: с CRLF не совпал бы ни один якорь строки.
    $Text = $Text -replace "`r`n", "`n"

    # package main -> package core
    $Text = [regex]::Replace($Text, '(?m)^package\s+main\s*$', 'package core')

    foreach ($rule in $replacements) {
        if ($rule.File -ne $Name) { continue }

        $found = [regex]::Matches($Text, $rule.Pattern)
        if ($found.Count -ne $rule.Count) {
            throw @"
Замена в $Name не сошлась: ожидалось совпадений $($rule.Count), найдено $($found.Count).
Апстрим изменил этот код — поправьте шаблон в scripts/vendor-core.ps1.
Шаблон: $($rule.Pattern)
"@
        }

        # Группа 1 — отступ совпавшей строки: вставка встаёт на тот же уровень.
        if ($rule.ContainsKey('Replace')) {
            $replacement = $rule.Replace
        } elseif ($rule.ContainsKey('InsertAfter')) {
            $replacement = '${0}' + "`n" + '${1}' + $rule.InsertAfter
        } elseif ($rule.ContainsKey('InsertBefore')) {
            $replacement = '${1}' + $rule.InsertBefore + "`n" + '${0}'
        } else {
            throw "Правило для $Name не задаёт ни Replace, ни InsertAfter, ни InsertBefore."
        }

        $Text = [regex]::Replace($Text, $rule.Pattern, $replacement)
    }

    if ($buildTags.ContainsKey($Name)) {
        $Text = $buildTags[$Name] + "`n`n" + $Text
    }

    return $Text
}

# --- сборка нового содержимого --------------------------------------------------

$version = Get-UpstreamVersion -Path $Upstream
$sourceFiles = Get-ChildItem -LiteralPath $source -Filter '*.go' -File | Sort-Object Name

if ($sourceFiles.Count -eq 0) {
    throw "В $source нет файлов *.go."
}

# Проверка счётчиков внутри Convert-CoreFile не сработает, если апстрим просто
# переименовал файл: правило тогда не выполнится ни разу и пропадёт молча.
$sourceNames = @($sourceFiles | ForEach-Object { $_.Name })
foreach ($rule in $replacements) {
    if ($rule.File -notin $sourceNames) {
        throw "Правило замены ссылается на $($rule.File), которого нет в $source."
    }
}

$header = @(
    '# Версия ядра qWDTT, перенесённого в internal/core.',
    '# Файл создаётся scripts/vendor-core.ps1 — руками не править.',
    '',
    'upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android',
    "commit   = $($version.Commit)",
    "date     = $($version.Date)",
    "files    = $($sourceFiles.Count)"
)
if ($version.Dirty) {
    $header += 'dirty    = true  # в апстриме есть незакоммиченные правки go_client'
}

$planned = [ordered]@{}
foreach ($file in $sourceFiles) {
    $text = Get-Content -LiteralPath $file.FullName -Raw -Encoding UTF8
    $planned[$file.Name] = Convert-CoreFile -Name $file.Name -Text $text
}
foreach ($name in ($generatedFiles.Keys | Sort-Object)) {
    $planned[$name] = $generatedFiles[$name]
}
$planned['CORE_VERSION'] = ($header -join "`n") + "`n"

# Имена, которыми распоряжается скрипт. Всё остальное в internal/core — наше.
$ownedNames = @($sourceFiles | ForEach-Object { $_.Name }) + @($generatedFiles.Keys)

# --- режим проверки -------------------------------------------------------------

if ($Check) {
    $problems = @()

    foreach ($name in $planned.Keys) {
        $path = Join-Path $target $name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            $problems += "нет файла: $name"
            continue
        }
        $current = (Get-Content -LiteralPath $path -Raw -Encoding UTF8) -replace "`r`n", "`n"
        if ($current -ne ($planned[$name] -replace "`r`n", "`n")) {
            $problems += "расходится: $name"
        }
    }

    if ($problems.Count -gt 0) {
        Write-Host "internal/core разошёлся с апстримом $($version.Commit):" -ForegroundColor Yellow
        $problems | ForEach-Object { Write-Host "  $_" }
        exit 1
    }

    Write-Host "internal/core совпадает с апстримом $($version.Commit)." -ForegroundColor Green
    exit 0
}

# --- запись ---------------------------------------------------------------------

if (-not (Test-Path -LiteralPath $target -PathType Container)) {
    New-Item -ItemType Directory -Path $target | Out-Null
}

# Апстримные файлы, которых больше нет, удаляем: иначе в сборке останется мусор.
$stale = Get-ChildItem -LiteralPath $target -Filter '*.go' -File |
    Where-Object { $_.Name -notin $ownedNames }
foreach ($file in $stale) {
    Write-Host "  не трогаю свой файл: $($file.Name)"
}

foreach ($name in $planned.Keys) {
    $path = Join-Path $target $name
    # UTF8 без BOM и с LF: так дифф против апстрима не забивается концами строк.
    $text = $planned[$name] -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

Write-Host "Перенесено из апстрима: $($sourceFiles.Count)" -ForegroundColor Green
Write-Host "Создано скриптом:       $($generatedFiles.Count)"
Write-Host "Апстрим: $($version.Commit) от $($version.Date)"
if ($version.Dirty) {
    Write-Host 'ВНИМАНИЕ: в апстриме есть незакоммиченные правки go_client.' -ForegroundColor Yellow
}
Write-Host ''
Write-Host 'Дальше: go mod tidy && go build ./...'
