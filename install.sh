#!/bin/sh
# Installs spnego-mcp-proxy on macOS and registers it as MCP server in
# Claude Code, VS Code and the GitHub Copilot app/CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/cwbr/spnego-mcp-proxy/main/install.sh | sh -s -- <name> <url>
#
# The whole script runs inside main() so a partially downloaded script
# does nothing. Commands get </dev/null so they cannot read the rest of the
# piped script from stdin.
set -eu

main() {
	repo=cwbr/spnego-mcp-proxy
	name=${1:-}
	url=${2:-}
	target="$HOME/.local/bin/spnego-mcp-proxy"

	[ "$(uname -s)" = Darwin ] || die "Dieses Skript ist nur für macOS. Für Windows gibt es install.ps1."
	if [ -z "$name" ] || [ -z "$url" ]; then
		die "Aufruf: curl -fsSL https://raw.githubusercontent.com/$repo/main/install.sh | sh -s -- <name> <url>"
	fi
	case $name in
	*[!A-Za-z0-9_-]*) die "Der Name darf nur Buchstaben, Ziffern, - und _ enthalten: $name" ;;
	esac
	case $url in
	http://* | https://*) ;;
	*) die "Die URL muss mit http:// oder https:// beginnen: $url" ;;
	esac
	case $(uname -m) in
	arm64) arch=arm64 ;;
	x86_64) arch=amd64 ;;
	*) die "Nicht unterstützte Architektur: $(uname -m)" ;;
	esac

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	install_binary
	check_ticket
	test_connection
	write_merge_script
	register_claude
	register_vscode
	register_copilot

	echo
	echo "Fertig. Die MCP-Clients neu starten, damit sie \"$name\" laden."
}

install_binary() {
	asset="spnego-mcp-proxy-darwin-$arch"
	base="https://github.com/$repo/releases/latest/download"
	info "Lade $asset"
	curl -fsSL -o "$tmp/$asset" "$base/$asset" </dev/null || die "Download fehlgeschlagen: $base/$asset"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" </dev/null || die "Download fehlgeschlagen: $base/checksums.txt"
	(cd "$tmp" && grep "  $asset\$" checksums.txt | shasum -a 256 -c -s) || die "Prüfsumme von $asset stimmt nicht."

	mkdir -p "$(dirname "$target")"
	chmod +x "$tmp/$asset"
	mv -f "$tmp/$asset" "$target"
	ok "Installiert: $target"
}

check_ticket() {
	if klist -s </dev/null 2>/dev/null; then
		ok "Kerberos-Ticket vorhanden"
	else
		warn "Kein gültiges Kerberos-Ticket gefunden. Bitte mit 'kinit' oder der Ticket-Viewer-App eines holen."
	fi
}

test_connection() {
	info "Teste Verbindung zu $url"
	init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"install","version":"0"}}}'
	if echo "$init" | MCP_UPSTREAM_URL="$url" "$target" >"$tmp/out" 2>"$tmp/err" && grep -q '"result"' "$tmp/out"; then
		ok "Verbindung erfolgreich"
		return
	fi
	message=$(sed -En 's/.*"message":"(([^"\\]|\\.)*)".*/\1/p' "$tmp/out" | sed 's/\\"/"/g' | head -n 1)
	[ -n "$message" ] || message=$(tail -n 1 "$tmp/err")
	warn "Verbindungstest fehlgeschlagen: $message"
	warn "Die Einrichtung wird trotzdem fortgesetzt. Hinweise: https://github.com/$repo#fehlersuche"
}

register_claude() {
	if ! command -v claude >/dev/null 2>&1; then
		info "Claude Code nicht gefunden, übersprungen"
		return
	fi
	claude mcp remove "$name" --scope user </dev/null >/dev/null 2>&1 || true
	if claude mcp add "$name" --scope user --env "MCP_UPSTREAM_URL=$url" -- "$target" </dev/null >/dev/null; then
		ok "Claude Code: \"$name\" eingetragen"
	else
		warn "Claude Code: Eintragen fehlgeschlagen. Manuell:"
		echo "    claude mcp add $name --scope user --env \"MCP_UPSTREAM_URL=$url\" -- $target"
	fi
}

register_vscode() {
	found=false
	for dir in "$HOME/Library/Application Support/Code/User" "$HOME/Library/Application Support/Code - Insiders/User"; do
		[ -d "$dir" ] || continue
		found=true
		merge_config "VS Code" "$dir/mcp.json" servers
	done
	$found || info "VS Code nicht gefunden, übersprungen"
}

# The GitHub Copilot app and Copilot CLI share ~/.copilot/mcp-config.json.
register_copilot() {
	dir=${COPILOT_HOME:-$HOME/.copilot}
	if [ ! -d "$dir" ]; then
		info "GitHub Copilot app/CLI nicht gefunden, übersprungen"
		return
	fi
	merge_config "GitHub Copilot app/CLI" "$dir/mcp-config.json" mcpServers
}

# merge_config adds the server under the given top-level key of a JSON
# config file. Other entries are kept and a backup is written first.
merge_config() {
	label=$1
	file=$2
	key=$3
	[ -f "$file" ] && cp "$file" "$file.bak"
	result=$(osascript -l JavaScript "$tmp/merge.js" "$file" "$key" "$name" "$target" "$url" </dev/null 2>&1) || true
	case $result in
	OK)
		ok "$label: \"$name\" in $file eingetragen"
		;;
	*)
		warn "$label: $file konnte nicht automatisch ergänzt werden ($(printf '%s\n' "$result" | head -n 1))."
		warn "Bitte diesen Eintrag unter \"$key\" einfügen:"
		printf '%s\n' "$result" | sed '1d'
		;;
	esac
}

info() { printf '==> %s\n' "$*"; }
ok() { printf ' ✓  %s\n' "$*"; }
warn() { printf ' !  %s\n' "$*" >&2; }
die() {
	printf 'Fehler: %s\n' "$*" >&2
	exit 1
}

# write_merge_script creates the JXA helper that adds the server to an MCP
# config file. It prints OK, or a reason and the entry for manual insertion.
# JSON.parse rejects files with comments, which are left untouched.
write_merge_script() {
	cat >"$tmp/merge.js" <<'EOF'
ObjC.import('Foundation');
function run(argv) {
	const [file, key, name, command, url] = argv;
	const entry = key === 'mcpServers'
		? { type: 'local', command: command, args: [], env: { MCP_UPSTREAM_URL: url }, tools: ['*'] }
		: { type: 'stdio', command: command, env: { MCP_UPSTREAM_URL: url } };
	try {
		const text = $.NSString.stringWithContentsOfFileEncodingError(file, $.NSUTF8StringEncoding, null);
		const config = text.isNil() || text.js.trim() === '' ? {} : JSON.parse(text.js);
		config[key] = config[key] || {};
		config[key][name] = entry;
		const json = $(JSON.stringify(config, null, '\t') + '\n');
		if (!json.writeToFileAtomicallyEncodingError(file, true, $.NSUTF8StringEncoding, null)) {
			throw new Error('cannot write ' + file);
		}
		return 'OK';
	} catch (error) {
		const snippet = JSON.stringify({ [name]: entry }, null, 2).split('\n').slice(1, -1).join('\n');
		return error.message + '\n' + snippet;
	}
}
EOF
}

main "$@"
