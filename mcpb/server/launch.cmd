@echo off
setlocal

set "corresync_arch=%PROCESSOR_ARCHITEW6432%"
if not defined corresync_arch set "corresync_arch=%PROCESSOR_ARCHITECTURE%"

if /I "%corresync_arch%"=="AMD64" (
  set "corresync_arch=amd64"
) else if /I "%corresync_arch%"=="ARM64" (
  set "corresync_arch=arm64"
) else (
  >&2 echo Corresync MCPB does not support this CPU architecture.
  exit /b 1
)

"%~dp0windows\%corresync_arch%\corr.exe" mcp serve
