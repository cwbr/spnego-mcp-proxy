package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// maxHandshakes limits fresh negotiations per request: one regular
	// handshake plus one retry when the server rejects the first one.
	maxHandshakes = 2
	// maxLegs limits the round trips of a single multi-step handshake.
	maxLegs = 5
)

// upstreamURL validates the configured MCP endpoint.
func upstreamURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	target, err := url.Parse(raw)
	if raw == "" || err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return nil, errors.New("MCP_UPSTREAM_URL must be an absolute HTTP(S) URL, e.g. https://mcp.example.internal/mcp")
	}
	return target, nil
}

// newHTTPClient returns a keep-alive HTTP/1.1 client. Negotiate is
// connection-oriented, so HTTP/2 stays disabled. TLS verification uses the
// operating system trust store.
func newHTTPClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// upstream sends MCP messages to the remote endpoint and keeps the session.
type upstream struct {
	url        string
	host       string
	client     *http.Client
	negotiator Negotiator

	mu              sync.Mutex
	sessionID       string
	protocolVersion string
}

func newUpstream(target *url.URL, client *http.Client, negotiator Negotiator) *upstream {
	return &upstream{url: target.String(), host: target.Hostname(), client: client, negotiator: negotiator}
}

func (u *upstream) session() (sessionID, protocolVersion string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sessionID, u.protocolVersion
}

func (u *upstream) setSessionID(sessionID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sessionID = sessionID
}

func (u *upstream) setProtocolVersion(version string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.protocolVersion = version
}

// do sends a request and authenticates with SPNEGO when the server asks for
// it. The returned response may still be a 401 if authentication failed.
func (u *upstream) do(ctx context.Context, method string, body []byte) (*http.Response, error) {
	resp, err := u.send(ctx, method, body, "")
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < maxHandshakes && isNegotiateChallenge(resp); attempt++ {
		discard(resp)
		if resp, err = u.handshake(ctx, method, body); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (u *upstream) handshake(ctx context.Context, method string, body []byte) (*http.Response, error) {
	sc, err := u.negotiator.NewContext(u.host)
	if err != nil {
		return nil, err
	}
	defer sc.Close()

	var serverToken []byte
	for leg := 0; leg < maxLegs; leg++ {
		clientToken, done, err := sc.Step(serverToken)
		if err != nil {
			return nil, err
		}
		resp, err := u.send(ctx, method, body, "Negotiate "+base64.StdEncoding.EncodeToString(clientToken))
		if err != nil {
			return nil, err
		}
		found, token, err := negotiateChallenge(resp.Header)
		if err != nil {
			log.Print(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			if found && len(token) > 0 && !done {
				if _, _, err := sc.Step(token); err != nil {
					log.Printf("mutual authentication of the server failed: %v", err)
				}
			}
			return resp, nil
		}
		if !found || len(token) == 0 || done {
			// Rejected without a continuation token.
			return resp, nil
		}
		discard(resp)
		serverToken = token
	}
	return nil, errors.New("SPNEGO handshake did not complete")
}

func (u *upstream) send(ctx context.Context, method string, body []byte, authorization string) (*http.Response, error) {
	var reader io.Reader = http.NoBody
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	sessionID, protocolVersion := u.session()
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return u.client.Do(req)
}

// closeSession ends the upstream session (best effort).
func (u *upstream) closeSession(timeout time.Duration) {
	if sessionID, _ := u.session(); sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := u.do(ctx, http.MethodDelete, nil)
	if err != nil {
		log.Printf("closing MCP session failed: %v", err)
		return
	}
	discard(resp)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusMethodNotAllowed {
		log.Printf("closing MCP session returned HTTP %d", resp.StatusCode)
	}
}

func isNegotiateChallenge(resp *http.Response) bool {
	if resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	found, _, _ := negotiateChallenge(resp.Header)
	return found
}

// discard drains a small remainder of the body so the connection can be
// reused, then closes it.
func discard(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// statusError describes a failed HTTP status for the MCP client.
func statusError(resp *http.Response, sessionSent bool) string {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "upstream rejected SPNEGO authentication (HTTP 401); check the Kerberos login of the current user"
	case resp.StatusCode == http.StatusNotFound && sessionSent:
		return "upstream MCP session expired or unknown (HTTP 404); restart the MCP server"
	default:
		return fmt.Sprintf("upstream returned HTTP %s", resp.Status)
	}
}
