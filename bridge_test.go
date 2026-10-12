package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// fakeNegotiator replaces SSPI/GSS in tests. Each context returns the
// configured client tokens in order and records the server tokens it got.
type fakeNegotiator struct {
	tokens []string

	mu           sync.Mutex
	hosts        []string
	serverTokens []string
}

func (n *fakeNegotiator) NewContext(host string) (SecurityContext, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hosts = append(n.hosts, host)
	return &fakeContext{negotiator: n}, nil
}

func (n *fakeNegotiator) contexts() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.hosts)
}

type fakeContext struct {
	negotiator *fakeNegotiator
	step       int
}

func (c *fakeContext) Step(serverToken []byte) ([]byte, bool, error) {
	n := c.negotiator
	n.mu.Lock()
	defer n.mu.Unlock()
	if serverToken != nil {
		n.serverTokens = append(n.serverTokens, string(serverToken))
	}
	if c.step >= len(n.tokens) {
		return nil, true, nil
	}
	token := n.tokens[c.step]
	c.step++
	return []byte(token), c.step == len(n.tokens), nil
}

func (c *fakeContext) Close() {}

func negotiateHeader(token string) string {
	return "Negotiate " + base64.StdEncoding.EncodeToString([]byte(token))
}

func challenge(w http.ResponseWriter, token string) {
	if token == "" {
		w.Header().Add("WWW-Authenticate", "Negotiate")
	} else {
		w.Header().Add("WWW-Authenticate", negotiateHeader(token))
	}
	w.Header().Add("WWW-Authenticate", `Basic realm="example"`)
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, "<html>Unauthorized</html>")
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

// fakeMCP is a minimal Streamable HTTP MCP server behind SPNEGO.
type fakeMCP struct {
	t       *testing.T
	release chan struct{}

	mu             sync.Mutex
	deletedSession string
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != negotiateHeader("client-token") {
		challenge(w, "")
		return
	}
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deletedSession = r.Header.Get("Mcp-Session-Id")
		f.mu.Unlock()
		return
	}
	if accept := r.Header.Get("Accept"); accept != "application/json, text/event-stream" {
		http.Error(w, "bad Accept: "+accept, http.StatusBadRequest)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\n  \"jsonrpc\": \"2.0\",\n  \"id\": %s,\n  \"result\": {\"protocolVersion\": \"2025-06-18\", \"capabilities\": {}}\n}\n", req.ID)
		return
	}
	if r.Header.Get("Mcp-Session-Id") != "sess-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
		http.Error(w, "missing session headers", http.StatusBadRequest)
		return
	}

	switch req.Method {
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": stream opened\n\nid: 0\ndata:\n\n")
		io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-f.release:
		case <-time.After(testTimeout):
			f.t.Error("stream was not released")
		}
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\ndata:  \"id\":%s,\ndata:  \"result\":{\"tools\":[]}}\n\n", req.ID)
	default:
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
	}
}

func (f *fakeMCP) deleted() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletedSession
}

type chanWriter chan string

func (c chanWriter) Write(p []byte) (int, error) {
	c <- string(p)
	return len(p), nil
}

type harness struct {
	t      *testing.T
	stdin  *io.PipeWriter
	lines  chanWriter
	done   chan struct{}
	cancel context.CancelFunc
}

func startBridge(t *testing.T, serverURL string, negotiator Negotiator) *harness {
	t.Helper()
	target, err := upstreamURL(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, stdin: stdinWriter, lines: make(chanWriter, 100), done: make(chan struct{}), cancel: cancel}
	b := newBridge(newUpstream(target, newHTTPClient(), negotiator), h.lines)
	go func() {
		b.run(ctx, stdinReader)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		stdinWriter.Close()
		<-h.done
	})
	return h
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.stdin, line+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) next() map[string]any {
	h.t.Helper()
	select {
	case line := <-h.lines:
		if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
			h.t.Fatalf("output is not exactly one line: %q", line)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			h.t.Fatalf("output is not JSON: %q", line)
		}
		return msg
	case <-time.After(testTimeout):
		h.t.Fatal("timed out waiting for output")
		return nil
	}
}

func (h *harness) expectNoOutput() {
	h.t.Helper()
	select {
	case line := <-h.lines:
		h.t.Fatalf("unexpected output: %q", line)
	case <-time.After(200 * time.Millisecond):
	}
}

func (h *harness) closeStdin() {
	h.t.Helper()
	h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(testTimeout):
		h.t.Fatal("bridge did not stop after stdin was closed")
	}
}

func expectError(t *testing.T, msg map[string]any, id any, code float64, contains string) {
	t.Helper()
	rpcErr, ok := msg["error"].(map[string]any)
	if !ok || msg["id"] != id || rpcErr["code"] != code || !strings.Contains(rpcErr["message"].(string), contains) {
		t.Fatalf("got %v, want error id=%v code=%v containing %q", msg, id, code, contains)
	}
}

func TestBridgeSession(t *testing.T) {
	server := &fakeMCP{t: t, release: make(chan struct{})}
	upstream := httptest.NewServer(server)
	defer upstream.Close()
	negotiator := &fakeNegotiator{tokens: []string{"client-token"}}
	h := startBridge(t, upstream.URL+"/mcp?client=1", negotiator)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if msg := h.next(); msg["id"] != 1.0 || msg["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("unexpected initialize response %v", msg)
	}

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.expectNoOutput()

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if msg := h.next(); msg["method"] != "notifications/progress" {
		t.Fatalf("expected streamed progress notification, got %v", msg)
	}
	close(server.release) // the response is only sent after the first event arrived
	if msg := h.next(); msg["id"] != 2.0 || msg["result"] == nil {
		t.Fatalf("unexpected tools/list response %v", msg)
	}

	h.closeStdin()
	if got := server.deleted(); got != "sess-1" {
		t.Errorf("DELETE carried session %q, want sess-1", got)
	}
	for _, host := range negotiator.hosts {
		if host != "127.0.0.1" {
			t.Errorf("negotiated for host %q, want 127.0.0.1", host)
		}
	}
}

func TestBridgeUnauthorized(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge(w, "")
	}))
	defer upstream.Close()
	negotiator := &fakeNegotiator{tokens: []string{"rejected-token"}}
	h := startBridge(t, upstream.URL, negotiator)

	h.send(`{"jsonrpc":"2.0","id":"a-5","method":"tools/list"}`)
	expectError(t, h.next(), "a-5", -32000, "HTTP 401")
	if got := negotiator.contexts(); got != maxHandshakes {
		t.Errorf("got %d handshakes, want %d", got, maxHandshakes)
	}

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.expectNoOutput()
}

func TestBridgeMultiLegHandshake(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case negotiateHeader("leg-1"):
			challenge(w, "server-challenge")
		case negotiateHeader("leg-2"):
			w.Header().Set("WWW-Authenticate", negotiateHeader("mutual-auth"))
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		default:
			challenge(w, "")
		}
	}))
	defer upstream.Close()
	negotiator := &fakeNegotiator{tokens: []string{"leg-1", "leg-2"}}
	h := startBridge(t, upstream.URL, negotiator)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if msg := h.next(); msg["id"] != 1.0 || msg["error"] != nil {
		t.Fatalf("unexpected response %v", msg)
	}
	if got := strings.Join(negotiator.serverTokens, ","); got != "server-challenge" {
		t.Errorf("server tokens passed to the context: %q", got)
	}
}

func TestBridgeExpiredSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") != "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Mcp-Session-Id", "old")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`)
	}))
	defer upstream.Close()
	h := startBridge(t, upstream.URL, &fakeNegotiator{})

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.next()
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	expectError(t, h.next(), 2.0, -32000, "session expired")
}

func TestBridgeRelaysJSONRPCErrorWithHTTPStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"invalid params"}}`)
	}))
	defer upstream.Close()
	h := startBridge(t, upstream.URL, &fakeNegotiator{})

	h.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call"}`)
	expectError(t, h.next(), 3.0, -32602, "invalid params")
	h.expectNoOutput()
}

func TestBridgeNetworkErrorAndParseError(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	url := upstream.URL
	upstream.Close()
	h := startBridge(t, url, &fakeNegotiator{})

	h.send(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	expectError(t, h.next(), 4.0, -32000, "upstream request failed")

	h.send(`not json`)
	expectError(t, h.next(), nil, -32700, "parse error")
}

func TestBridgeCancelAbortsStream(t *testing.T) {
	server := &fakeMCP{t: t, release: make(chan struct{})}
	upstream := httptest.NewServer(server)
	defer upstream.Close()
	defer close(server.release) // unblocks the handler before the server closes
	h := startBridge(t, upstream.URL, &fakeNegotiator{tokens: []string{"client-token"}})

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.next()
	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	h.next() // progress notification; the stream now blocks

	h.cancel()
	select {
	case <-h.done:
	case <-time.After(testTimeout):
		t.Fatal("bridge did not stop after cancellation")
	}
	if got := server.deleted(); got != "sess-1" {
		t.Errorf("DELETE carried session %q, want sess-1", got)
	}
}
