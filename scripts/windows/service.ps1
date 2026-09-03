<#
Wiki-Brain Windows 服务管理脚本
用法：service.ps1 install | uninstall | start | stop | restart | status

依赖 NSSM（https://nssm.cc/）把普通控制台程序包装成 Windows 服务——
Wiki-Brain 是纯 Go 编译的控制台程序，没有实现 Windows Service Control
Manager 需要的回调接口，不能直接用 `sc create` 稳定运行，所以借助 NSSM。

使用前请把 nssm.exe 放在本脚本同一目录下（跟 wiki-brain-server.exe 平级），
或确保 nssm 已在系统 PATH 中；nssm.exe 本身体积很小，从
https://nssm.cc/download 下载对应架构（win32/win64）解压即可，Wiki-Brain
的构建脚本不会自动下载它。

install/uninstall 需要管理员权限运行本脚本（右键"以管理员身份运行"，
或从管理员 PowerShell 里执行）。
#>

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet("install", "uninstall", "start", "stop", "restart", "status")]
    [string]$Action = "status",

    [string]$NssmPath
)

$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ServiceName = "WikiBrain"
$DisplayName = "Wiki-Brain Server"
$BinPath = Join-Path $RootDir "wiki-brain-server.exe"
$ConfigPath = Join-Path $RootDir "config\config.yml"
$LogDir = Join-Path $RootDir "logs"

function Resolve-Nssm {
    if ($NssmPath) {
        if (Test-Path $NssmPath) { return (Resolve-Path $NssmPath).Path }
        throw "指定的 -NssmPath 不存在：$NssmPath"
    }

    $local = Join-Path $RootDir "nssm.exe"
    if (Test-Path $local) { return $local }

    $cmd = Get-Command nssm.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    throw @"
未找到 nssm.exe。请从 https://nssm.cc/download 下载对应架构（win64 多数情况下适用），
把 nssm.exe 放到本脚本同目录（$RootDir）下，或加入系统 PATH 后重试。
也可以用 -NssmPath 参数显式指定路径，例如：
  .\service.ps1 install -NssmPath C:\tools\nssm\nssm.exe
"@
}

function Assert-Admin {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "此操作需要管理员权限，请以管理员身份重新运行本脚本（右键 -> 以管理员身份运行 PowerShell）。"
    }
}

function Get-ConfiguredPort {
    if (-not (Test-Path $ConfigPath)) { return $null }
    $line = Select-String -Path $ConfigPath -Pattern '^\s*port:\s*(\d+)' | Select-Object -First 1
    if ($line) { return $line.Matches[0].Groups[1].Value }
    return $null
}

function Install-WikiBrainService {
    Assert-Admin
    if (-not (Test-Path $BinPath)) {
        throw "找不到可执行文件：$BinPath"
    }

    $nssm = Resolve-Nssm
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "服务 $ServiceName 已存在，先移除旧服务再重新安装 ..."
        & $nssm stop $ServiceName 2>$null | Out-Null
        & $nssm remove $ServiceName confirm | Out-Null
    }

    & $nssm install $ServiceName $BinPath
    & $nssm set $ServiceName AppParameters "-config `"$ConfigPath`""
    & $nssm set $ServiceName AppDirectory $RootDir
    & $nssm set $ServiceName DisplayName $DisplayName
    & $nssm set $ServiceName Description "Wiki-Brain 知识检索服务"
    & $nssm set $ServiceName Start SERVICE_AUTO_START
    & $nssm set $ServiceName AppStdout (Join-Path $LogDir "service-stdout.log")
    & $nssm set $ServiceName AppStderr (Join-Path $LogDir "service-stderr.log")
    & $nssm set $ServiceName AppRotateFiles 1
    & $nssm set $ServiceName AppRotateOnline 1
    & $nssm set $ServiceName AppRotateBytes 10485760
    & $nssm set $ServiceName AppExit Default Restart

    Write-Host "服务 $ServiceName 安装完成。用 '.\service.ps1 start' 启动。"
}

function Uninstall-WikiBrainService {
    Assert-Admin
    $nssm = Resolve-Nssm

    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $existing) {
        Write-Host "服务 $ServiceName 不存在，无需卸载。"
        return
    }

    & $nssm stop $ServiceName 2>$null | Out-Null
    & $nssm remove $ServiceName confirm
    Write-Host "服务 $ServiceName 已卸载。"
}

function Start-WikiBrainService {
    $port = Get-ConfiguredPort
    Start-Service -Name $ServiceName
    Write-Host "已发起启动，等待端口就绪 ..."

    $waited = 0
    while ($waited -lt 20) {
        $svc = Get-Service -Name $ServiceName
        if ($svc.Status -eq "Running") {
            if ($port -and -not (Test-NetConnection -ComputerName 127.0.0.1 -Port $port -InformationLevel Quiet -WarningAction SilentlyContinue)) {
                Start-Sleep -Seconds 1
                $waited++
                continue
            }
            break
        }
        Start-Sleep -Seconds 1
        $waited++
    }

    Show-Status
}

function Stop-WikiBrainService {
    Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
    Write-Host "服务 $ServiceName 已停止。"
}

function Show-Status {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) {
        Write-Host "服务 $ServiceName 未安装。"
        return
    }
    $port = Get-ConfiguredPort
    Write-Host "服务 $ServiceName 状态：$($svc.Status)$(if ($port) { "（端口 $port）" })"
}

switch ($Action) {
    "install"   { Install-WikiBrainService }
    "uninstall" { Uninstall-WikiBrainService }
    "start"     { Start-WikiBrainService }
    "stop"      { Stop-WikiBrainService }
    "restart"   { Stop-WikiBrainService; Start-WikiBrainService }
    "status"    { Show-Status }
}
