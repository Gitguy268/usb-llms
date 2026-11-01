@echo off
rem Double-click on Windows.
cd /d "%~dp0"
set "ARCH=amd64"
if /i "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "ARCH=arm64"
if not exist "bin\usb-llms-windows-%ARCH%.exe" set "ARCH=amd64"
if not exist "bin\usb-llms-windows-%ARCH%.exe" (
  echo This folder has no USB LLMS program for Windows. Download a full release.
  pause
  exit /b 1
)
"bin\usb-llms-windows-%ARCH%.exe" %*
if errorlevel 1 pause
