@echo off
rem Runs deploy-windows.ps1 with a one-off execution policy bypass (machine policy is unchanged).
rem Double-click or run from any prompt; asks for administrator rights when needed.
net session >nul 2>&1
if errorlevel 1 (
  powershell -NoProfile -Command "Start-Process powershell -Verb RunAs -ArgumentList '-NoProfile -NoExit -ExecutionPolicy Bypass -File \"%~dp0deploy-windows.ps1\"'"
  exit /b
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0deploy-windows.ps1" %*
