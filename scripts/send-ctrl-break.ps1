<#
.SYNOPSIS
    Посылает Ctrl+Break консольному процессу по его PID — остановить qwdtt-cli,
    запущенный в фоне, так же, как его остановил бы Ctrl+C.

.DESCRIPTION
    Нужен для живых проверок из Git Bash и других оболочек, где qwdtt-cli запущен
    в фоне. `kill -INT` из Git Bash до Windows-процесса не долетает, а фоновый
    процесс живёт в своей группе, где Ctrl+C отключён. Ctrl+Break в группу
    по PID доходит, и Go переводит его в тот же SIGINT, что и Ctrl+C: qwdtt-cli
    останавливает туннель и убирает за собой.

    Скрипт отключается от своей консоли, подключается к консоли процесса,
    снимает с себя обработку сигнала и генерирует CTRL_BREAK_EVENT.

    PID — Windows-PID из tasklist, а не $! из Git Bash: там msys-PID.

    Звать из другого вызова оболочки, чем тот, что запустил qwdtt-cli. Если qwdtt-cli и
    скрипт запущены одной командной строкой, они в одной группе процессов
    консоли, и Ctrl+Break достаётся и оболочке: qwdtt-cli остановится чисто, но
    оболочка умрёт вместе с ним (проверено).

.PARAMETER ProcessId
    Windows-PID процесса, лидера группы.

.EXAMPLE
    tasklist /FI "IMAGENAME eq qwdtt-cli.exe"
    pwsh scripts/send-ctrl-break.ps1 -ProcessId 31180
#>
[CmdletBinding()]
param([Parameter(Mandatory)][int] $ProcessId)

$ErrorActionPreference = 'Stop'

Add-Type -Namespace QwdttCli -Name ConsoleCtrl -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool FreeConsole();
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool AttachConsole(uint dwProcessId);
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool SetConsoleCtrlHandler(IntPtr handler, bool add);
[DllImport("kernel32.dll", SetLastError = true)] public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);
'@

$CTRL_BREAK_EVENT = 1

[QwdttCli.ConsoleCtrl]::FreeConsole() | Out-Null
if (-not [QwdttCli.ConsoleCtrl]::AttachConsole([uint32]$ProcessId)) {
    throw "AttachConsole($ProcessId): ошибка $([Runtime.InteropServices.Marshal]::GetLastWin32Error())"
}
# Без этого сигнал, посланный в группу, достался бы и самому скрипту.
[QwdttCli.ConsoleCtrl]::SetConsoleCtrlHandler([IntPtr]::Zero, $true) | Out-Null
if (-not [QwdttCli.ConsoleCtrl]::GenerateConsoleCtrlEvent($CTRL_BREAK_EVENT, [uint32]$ProcessId)) {
    throw "GenerateConsoleCtrlEvent: ошибка $([Runtime.InteropServices.Marshal]::GetLastWin32Error())"
}
Start-Sleep -Milliseconds 500
[QwdttCli.ConsoleCtrl]::FreeConsole() | Out-Null
