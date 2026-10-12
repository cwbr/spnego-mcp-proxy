# spnego-mcp-proxy

Kleine, eigenständige Binary für Windows und macOS. Sie ist ein stdio-MCP-Server
und leitet JSON-RPC-Nachrichten an einen entfernten MCP-Server (Streamable HTTP)
weiter. Die Anmeldung dort läuft per SPNEGO/Kerberos mit dem SSO des
angemeldeten Users:

- **Windows:** SSPI („Negotiate“) mit der Domänenanmeldung, kein Passwort
- **macOS:** GSS.framework mit dem Kerberos-Ticket des Users (`klist`)

MCP-Clients wie VS Code/Copilot oder Claude Code beantworten keine
`WWW-Authenticate: Negotiate`-Challenge. Ohne Proxy endet die Verbindung zu
einem SPNEGO-geschützten MCP-Server mit HTTP 401.

Die Konfiguration erfolgt ausschließlich über die Umgebungsvariable
`MCP_UPSTREAM_URL` (absolute http/https-URL des MCP-Endpunkts). Passwörter oder
Tokens werden weder gespeichert noch geloggt. Die TLS-Prüfung bleibt immer aktiv
und nutzt den Zertifikatsspeicher des Betriebssystems (Windows-Zertifikatsspeicher
bzw. macOS-Schlüsselbund). Als Kerberos-SPN wird `HTTP/<host>` aus der URL
verwendet.

In den Beispielen steht `https://mcp.example.internal/mcp` für den eigenen
Endpunkt. Mehrere MCP-Server lassen sich mit derselben Binary unter
verschiedenen Namen und URLs eintragen.

## Build (auf dem Mac)

Voraussetzungen: Go ≥ 1.25 und die Xcode Command Line Tools.

```sh
./build.sh
```

Das Skript führt `go vet` und die Tests aus und erzeugt dann:

- `dist/windows-amd64/spnego-mcp-proxy.exe` (statisch, ohne cgo)
- `dist/windows-arm64/spnego-mcp-proxy.exe`
- `dist/macos-arm64/spnego-mcp-proxy` (Apple Silicon, gegen GSS.framework gelinkt)
- `dist/macos-amd64/spnego-mcp-proxy` (Intel, gegen GSS.framework gelinkt)

## Windows

### Ablage

Die exe nach `%LOCALAPPDATA%\spnego-mcp-proxy\spnego-mcp-proxy.exe` kopieren.
Eine Installation oder Admin-Rechte sind nicht nötig.

### VS Code (`mcp.json`)

```json
"myServer": {
  "type": "stdio",
  "command": "C:\\Users\\<kennung>\\AppData\\Local\\spnego-mcp-proxy\\spnego-mcp-proxy.exe",
  "env": {
    "MCP_UPSTREAM_URL": "https://mcp.example.internal/mcp"
  }
}
```

### Claude Code

```powershell
claude mcp add --scope user --env MCP_UPSTREAM_URL="https://mcp.example.internal/mcp" myServer -- "C:\Users\<kennung>\AppData\Local\spnego-mcp-proxy\spnego-mcp-proxy.exe"
```

### Manueller Test (PowerShell)

```powershell
$env:MCP_UPSTREAM_URL = "https://mcp.example.internal/mcp"
'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' | .\spnego-mcp-proxy.exe
```

Erwartet wird eine einzeilige JSON-Antwort mit `"result":{...,"serverInfo":...}`.

## macOS

### Ablage

Passende Binary wählen (`uname -m` liefert `arm64` oder `x86_64`):

```sh
mkdir -p ~/.local/bin
cp dist/macos-arm64/spnego-mcp-proxy ~/.local/bin/   # Intel: dist/macos-amd64/
```

Selbst gebaute Binaries laufen sofort. Wurde die Datei per Browser oder Chat
heruntergeladen, blockiert Gatekeeper sie. In dem Fall
`xattr -d com.apple.quarantine ~/.local/bin/spnego-mcp-proxy` ausführen.

Ein gültiges Kerberos-Ticket muss vorhanden sein (`klist`). Ist keines da,
eines per `kinit` oder über die Ticket-Viewer-App holen.

### VS Code (`mcp.json`)

```json
"myServer": {
  "type": "stdio",
  "command": "/Users/<kennung>/.local/bin/spnego-mcp-proxy",
  "env": {
    "MCP_UPSTREAM_URL": "https://mcp.example.internal/mcp"
  }
}
```

### Claude Code

```sh
claude mcp add --scope user --env MCP_UPSTREAM_URL="https://mcp.example.internal/mcp" myServer -- ~/.local/bin/spnego-mcp-proxy
```

### Manueller Test

```sh
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' \
  | MCP_UPSTREAM_URL="https://mcp.example.internal/mcp" ~/.local/bin/spnego-mcp-proxy
```

## Fehlersuche

Logs landen auf stderr, in VS Code unter `MCP: List Servers` > `Show Output`.

- **HTTP 401 / „rejected SPNEGO authentication“:** Es ist kein gültiges
  Kerberos-Ticket für `HTTP/<host>` vorhanden.
  - Windows: mit dem Domänenkonto angemeldet? `klist` zeigt die Tickets,
    `klist get HTTP/<host>` fordert das Service-Ticket gezielt an.
  - macOS: `klist` prüfen, gegebenenfalls `kinit <kennung>@<REALM>` ausführen.
  - Fehlt der SPN auf Serverseite oder ist er falsch, muss der Betreiber des
    Servers ihn einrichten.
- **Zertifikatsfehler (`x509: ...`):** Die interne CA fehlt im
  Zertifikatsspeicher bzw. Schlüsselbund. Die IT soll sie verteilen. Die
  Prüfung lässt sich bewusst nicht abschalten.
- **Timeout / `dial tcp ...`:** VPN bzw. Firmennetz prüfen und ob der Host
  auflösbar ist (`nslookup <host>`). Ein in der Umgebung gesetzter
  `HTTPS_PROXY` wird verwendet. Für interne Hosts gegebenenfalls `NO_PROXY`
  setzen.
- **HTTP 404 „session expired“:** Die MCP-Session auf dem Server ist abgelaufen.
  Den MCP-Server im Client neu starten.
- **Windows SmartScreen / „Der Computer wurde durch Windows geschützt“:** Die
  exe ist nicht signiert. Über „Weitere Informationen“ > „Trotzdem ausführen“
  starten oder in den Dateieigenschaften „Zulassen“ anhaken. Erlaubt eine
  AppLocker- oder Defender-Richtlinie keine unsignierten Programme, ist eine
  Signatur bzw. Freigabe durch die IT nötig.

## Entwicklung

```sh
go vet ./... && GOOS=windows go vet ./...
go test -race ./...
```

Die Tests ersetzen SSPI/GSS durch einen Fake (`Negotiator`-Interface) und
nutzen einen `httptest`-Server.
