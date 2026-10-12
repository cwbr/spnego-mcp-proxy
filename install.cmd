@echo off
setlocal
rem Installs spnego-mcp-proxy and registers it as MCP server in Claude Code
rem and VS Code. Double-click and answer the prompts, or call
rem   install.cmd <name> <url>
rem Name and URL can also be preset in the two lines below.
rem The work is done by install.ps1 from the repository; PowerShell is part
rem of every Windows installation.

set "MCP_NAME="
set "MCP_URL="

if not "%~1"=="" set "MCP_NAME=%~1"
if not "%~2"=="" set "MCP_URL=%~2"
if not defined MCP_NAME set /p "MCP_NAME=Name des MCP-Servers (z. B. myServer): "
if not defined MCP_URL set /p "MCP_URL=MCP-URL (https://...): "

rem Name and URL are passed as environment variables so that characters
rem like & in the URL cannot break the command line.
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((Invoke-RestMethod https://raw.githubusercontent.com/cwbr/spnego-mcp-proxy/main/install.ps1))) -Name $env:MCP_NAME -Url $env:MCP_URL"
set "RC=%ERRORLEVEL%"

echo.
pause
exit /b %RC%
