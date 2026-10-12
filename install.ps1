<#
.SYNOPSIS
Installs spnego-mcp-proxy on Windows and registers it as MCP server in
Claude Code, VS Code and the GitHub Copilot app/CLI.

.EXAMPLE
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/cwbr/spnego-mcp-proxy/main/install.ps1))) -Name myServer -Url https://mcp.example.internal/mcp
#>
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9_-]+$')]
    [string] $Name,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^https?://')]
    [string] $Url
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$repo = 'cwbr/spnego-mcp-proxy'
$installDir = Join-Path $env:LOCALAPPDATA 'spnego-mcp-proxy'
$exe = Join-Path $installDir 'spnego-mcp-proxy.exe'

function Write-Info([string] $Text) { Write-Host "==> $Text" }
function Write-Ok([string] $Text) { Write-Host " OK $Text" -ForegroundColor Green }
function Write-Warn([string] $Text) { Write-Host " !  $Text" -ForegroundColor Yellow }

# Native architecture, also correct when PowerShell runs emulated on ARM64.
function Get-Arch {
    try {
        $native = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').PROCESSOR_ARCHITECTURE
    } catch {
        $native = $env:PROCESSOR_ARCHITECTURE
    }
    if ($native -eq 'ARM64') { 'arm64' } else { 'amd64' }
}

# Runs a native command without turning its stderr into terminating errors.
function Invoke-Native([scriptblock] $Command) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $Command 2>&1 | Out-String
        [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = $output }
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Install-Binary {
    $asset = "spnego-mcp-proxy-windows-$(Get-Arch).exe"
    $base = "https://github.com/$repo/releases/latest/download"
    New-Item -ItemType Directory -Force $installDir | Out-Null
    $download = "$exe.download"
    $checksums = Join-Path $installDir 'checksums.txt'

    Write-Info "Lade $asset"
    Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile $download
    Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile $checksums
    $line = Get-Content $checksums | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } | Select-Object -First 1
    Remove-Item $checksums
    $expected = if ($line) { ($line -split '\s+')[0] } else { '' }
    $actual = (Get-FileHash -Algorithm SHA256 $download).Hash
    if (-not $expected -or $actual -ne $expected) {
        Remove-Item $download
        throw "Prüfsumme von $asset stimmt nicht."
    }

    # A running exe cannot be overwritten but can be renamed, so running
    # MCP servers keep working until they are restarted.
    if (Test-Path $exe) {
        Remove-Item "$exe.old" -Force -ErrorAction SilentlyContinue
        Move-Item -Force $exe "$exe.old"
    }
    Move-Item -Force $download $exe
    Remove-Item "$exe.old" -Force -ErrorAction SilentlyContinue
    Unblock-File $exe
    Write-Ok "Installiert: $exe"
}

function Test-Upstream {
    Write-Info "Teste Verbindung zu $Url"
    $request = Join-Path $installDir 'test-request.json'
    $stdout = Join-Path $installDir 'test-stdout.txt'
    $stderr = Join-Path $installDir 'test-stderr.txt'
    $init = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"install","version":"0"}}}'
    [IO.File]::WriteAllText($request, "$init`n")

    $env:MCP_UPSTREAM_URL = $Url
    try {
        $process = Start-Process -FilePath $exe -NoNewWindow -PassThru `
            -RedirectStandardInput $request -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        if (-not $process.WaitForExit(60000)) {
            $process.Kill()
        }
        $out = Get-Content -Raw $stdout -ErrorAction SilentlyContinue
        $err = Get-Content $stderr -ErrorAction SilentlyContinue
    } catch {
        $out = ''
        $err = $_.Exception.Message
    } finally {
        Remove-Item Env:MCP_UPSTREAM_URL -ErrorAction SilentlyContinue
        Remove-Item $request, $stdout, $stderr -ErrorAction SilentlyContinue
    }

    if ($out -match '"result"') {
        Write-Ok 'Verbindung erfolgreich'
        return
    }
    if ($out -match '"message":"((?:[^"\\]|\\.)*)"') {
        $message = $Matches[1] -replace '\\"', '"'
    } else {
        $message = @($err)[-1]
    }
    Write-Warn "Verbindungstest fehlgeschlagen: $message"
    Write-Warn "Die Einrichtung wird trotzdem fortgesetzt. Hinweise: https://github.com/$repo#fehlersuche"
}

function Register-Claude {
    $claude = Get-Command claude -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $claude) {
        Write-Info 'Claude Code nicht gefunden, übersprungen'
        return
    }
    $manual = "claude mcp add $Name --scope user --env `"MCP_UPSTREAM_URL=$Url`" -- `"$exe`""
    # cmd.exe shims would interpret "&" in the URL.
    if ($claude.Source -match '\.(cmd|bat)$' -and $Url.Contains('&')) {
        Write-Warn 'Claude Code: Die URL enthält "&", bitte manuell eintragen:'
        Write-Host "    $manual"
        return
    }
    Invoke-Native { claude mcp remove $Name --scope user } | Out-Null
    $result = Invoke-Native { claude mcp add $Name --scope user --env "MCP_UPSTREAM_URL=$Url" -- $exe }
    if ($result.ExitCode -eq 0) {
        Write-Ok "Claude Code: `"$Name`" eingetragen"
    } else {
        Write-Warn "Claude Code: Eintragen fehlgeschlagen: $($result.Output.Trim())"
        Write-Host "    $manual"
    }
}

# Adds the server under the given top-level key of a JSON config file.
# Other entries are kept and a backup is written first.
function Add-McpServer([string] $Label, [string] $File, [string] $Key, $Entry) {
    try {
        $text = if (Test-Path $File) { [IO.File]::ReadAllText($File) } else { '' }
        # PowerShell 7 would accept comments and silently drop them on write.
        if ($text -match '(?m)^\s*//|/\*') { throw 'Datei enthält Kommentare' }
        $config = if ([string]::IsNullOrWhiteSpace($text)) { [pscustomobject]@{} } else { $text | ConvertFrom-Json }
        if (-not $config.PSObject.Properties[$Key]) {
            $config | Add-Member -NotePropertyName $Key -NotePropertyValue ([pscustomobject]@{})
        }
        $config.$Key | Add-Member -Force -NotePropertyName $Name -NotePropertyValue $Entry
        if (Test-Path $File) { Copy-Item -Force $File "$File.bak" }
        $json = $config | ConvertTo-Json -Depth 32
        [IO.File]::WriteAllText($File, "$json`n", (New-Object Text.UTF8Encoding $false))
        Write-Ok "${Label}: `"$Name`" in $File eingetragen"
    } catch {
        Write-Warn "${Label}: $File konnte nicht automatisch ergänzt werden ($($_.Exception.Message))."
        Write-Warn "Bitte diesen Eintrag unter `"$Key`" einfügen:"
        Write-Host ("`"$Name`": " + ($Entry | ConvertTo-Json -Depth 4))
    }
}

function Register-VSCode {
    $found = $false
    foreach ($product in 'Code', 'Code - Insiders') {
        $userDir = Join-Path $env:APPDATA "$product\User"
        if (-not (Test-Path $userDir)) { continue }
        $found = $true
        $entry = [ordered]@{ type = 'stdio'; command = $exe; env = [ordered]@{ MCP_UPSTREAM_URL = $Url } }
        Add-McpServer 'VS Code' (Join-Path $userDir 'mcp.json') 'servers' $entry
    }
    if (-not $found) { Write-Info 'VS Code nicht gefunden, übersprungen' }
}

# The GitHub Copilot app and Copilot CLI share ~/.copilot/mcp-config.json.
function Register-Copilot {
    $dir = if ($env:COPILOT_HOME) { $env:COPILOT_HOME } else { Join-Path $env:USERPROFILE '.copilot' }
    if (-not (Test-Path $dir)) {
        Write-Info 'GitHub Copilot app/CLI nicht gefunden, übersprungen'
        return
    }
    $entry = [ordered]@{
        type    = 'local'
        command = $exe
        args    = @()
        env     = [ordered]@{ MCP_UPSTREAM_URL = $Url }
        tools   = @('*')
    }
    Add-McpServer 'GitHub Copilot app/CLI' (Join-Path $dir 'mcp-config.json') 'mcpServers' $entry
}

Install-Binary
Test-Upstream
Register-Claude
Register-VSCode
Register-Copilot

Write-Host ''
Write-Host "Fertig. Die MCP-Clients neu starten, damit sie `"$Name`" laden."
