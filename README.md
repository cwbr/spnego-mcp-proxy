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

## Schnellinstallation

Ein Befehl lädt die passende Binary aus dem neuesten Release, prüft die
Prüfsumme, testet die Verbindung und trägt den Server in Claude Code und VS Code
ein, soweit installiert. Andere Einträge bleiben erhalten. Von einer vorhandenen
VS-Code-`mcp.json` wird vorher eine Kopie `mcp.json.bak` angelegt. Enthält die
Datei Kommentare, bleibt sie unverändert, und das Skript gibt den Eintrag zum
manuellen Einfügen aus. Ein vorhandener Eintrag mit demselben Namen wird ersetzt.

**Windows** (PowerShell, keine Admin-Rechte nötig):

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/cwbr/spnego-mcp-proxy/main/install.ps1))) -Name myServer -Url "https://mcp.example.internal/mcp"
```

Ohne PowerShell-Kenntnisse geht es auch per Doppelklick:
[`install.cmd`](https://github.com/cwbr/spnego-mcp-proxy/releases/latest/download/install.cmd)
herunterladen, doppelklicken und Name und URL eingeben. Die Datei ruft intern
dasselbe PowerShell-Skript auf. Name und URL lassen sich auch als Argumente
übergeben (`install.cmd myServer https://...`) oder in den Zeilen
`set "MCP_NAME="` und `set "MCP_URL="` oben in der Datei vorbelegen. Eine so
vorbelegte Datei kann man an Kollegen weitergeben, die dann nur noch
doppelklicken müssen. Wie bei der exe zeigt Windows für heruntergeladene Dateien
eventuell eine SmartScreen-Warnung.

**macOS** (Terminal):

```sh
curl -fsSL https://raw.githubusercontent.com/cwbr/spnego-mcp-proxy/main/install.sh | sh -s -- myServer "https://mcp.example.internal/mcp"
```

Die Binary landet unter `%LOCALAPPDATA%\spnego-mcp-proxy\spnego-mcp-proxy.exe`
bzw. `~/.local/bin/spnego-mcp-proxy`. Ein erneuter Aufruf aktualisiert sie auf
das neueste Release. Unter Windows funktioniert das auch, während MCP-Server
laufen; sie nutzen die neue Version nach einem Neustart.

Die manuelle Einrichtung beschreiben die Abschnitte [Windows](#windows) und
[macOS](#macos).

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

## Release (GitHub Actions)

Unter *Actions* > *Release* > *Run workflow* starten, mehr ist nicht nötig.
Ohne Angabe wird die Version ausgehend vom letzten Tag hochgezählt
(Standard `minor`, wählbar `patch`/`major`); optional lässt sich eine Version
wie `v1.2.0` vorgeben. Der Workflow läuft auf einem macOS-Runner, weil die
macOS-Binaries cgo brauchen. Er

1. führt `go vet` und die Tests aus,
2. setzt und pusht den Versions-Tag (es wird nichts auf `main` committet),
3. baut mit GoReleaser die Binaries und hängt sie samt `checksums.txt` an das
   GitHub-Release: `spnego-mcp-proxy-windows-amd64.exe`,
   `spnego-mcp-proxy-windows-arm64.exe`, `spnego-mcp-proxy-darwin-arm64`,
   `spnego-mcp-proxy-darwin-amd64`. Die Release-Notes entstehen aus den
   Commits seit dem letzten Tag.

Lokal lässt sich der Release-Build ohne Veröffentlichung prüfen:
`goreleaser release --snapshot --clean --skip=publish`.

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
claude mcp add myServer --scope user --env "MCP_UPSTREAM_URL=https://mcp.example.internal/mcp" -- "C:\Users\<kennung>\AppData\Local\spnego-mcp-proxy\spnego-mcp-proxy.exe"
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
claude mcp add myServer --scope user --env "MCP_UPSTREAM_URL=https://mcp.example.internal/mcp" -- ~/.local/bin/spnego-mcp-proxy
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
