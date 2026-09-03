@echo off
REM Wiki-Brain Windows 服务管理脚本（service.ps1 的双击入口）
REM 用法：service.bat install | uninstall | start | stop | restart | status
REM 不带参数双击运行时默认执行 status。

chcp 65001 >nul
setlocal

set "SCRIPT_DIR=%~dp0"
set "ACTION=%~1"
if "%ACTION%"=="" set "ACTION=status"

REM install/uninstall 等操作需要管理员权限；用 `net session` 探测当前是否
REM 已经是管理员，不是的话弹 UAC 重新以管理员身份打开一个新窗口执行，
REM 避免用户忘记右键"以管理员身份运行"导致操作静默失败。
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo 需要管理员权限，正在申请提升 ...
    powershell -NoProfile -ExecutionPolicy Bypass -Command ^
        "Start-Process cmd -Verb RunAs -ArgumentList '/c \"\"%~f0\" %ACTION%\"'"
    exit /b
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%service.ps1" %ACTION%

echo.
pause
endlocal
