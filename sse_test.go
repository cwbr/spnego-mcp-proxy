package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type sseEvent struct {
	event string
	data  string
}

func collectSSE(t *testing.T, stream string) []sseEvent {
	t.Helper()
	var events []sseEvent
	err := readSSE(strings.NewReader(stream), func(event string, data []byte) {
		events = append(events, sseEvent{event, string(data)})
	})
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	return events
}

func TestReadSSE(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []sseEvent
	}{
		{
			name:   "single event",
			stream: "data: {\"id\":1}\n\n",
			want:   []sseEvent{{"", `{"id":1}`}},
		},
		{
			name:   "multi-line data",
			stream: "data: {\"a\":\ndata: 1}\n\n",
			want:   []sseEvent{{"", "{\"a\":\n1}"}},
		},
		{
			name:   "comments, event name, id and retry",
			stream: ": keep-alive\nevent: message\nid: 7\nretry: 1000\ndata: x\n\n:ping\n\n",
			want:   []sseEvent{{"message", "x"}},
		},
		{
			name:   "CRLF line endings",
			stream: "data: a\r\ndata: b\r\n\r\ndata: c\r\n\r\n",
			want:   []sseEvent{{"", "a\nb"}, {"", "c"}},
		},
		{
			name:   "last event without blank line",
			stream: "data: first\n\ndata: second\n",
			want:   []sseEvent{{"", "first"}, {"", "second"}},
		},
		{
			name:   "last event without newline",
			stream: "data: only",
			want:   []sseEvent{{"", "only"}},
		},
		{
			name:   "value without space and empty data field",
			stream: "data:x\ndata\n\n",
			want:   []sseEvent{{"", "x\n"}},
		},
		{
			name:   "event without data is skipped",
			stream: "id: 1\n\nevent: message\n\n",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collectSSE(t, tt.stream); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyResponse(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		contentType   string
		contentLength int64
		want          responseKind
	}{
		{"accepted", http.StatusAccepted, "", 0, kindEmpty},
		{"accepted with content type", http.StatusAccepted, "application/json", -1, kindEmpty},
		{"no content", http.StatusNoContent, "", -1, kindEmpty},
		{"empty json", http.StatusOK, "application/json", 0, kindEmpty},
		{"json", http.StatusOK, "application/json", 42, kindJSON},
		{"json with charset", http.StatusOK, "application/json; charset=utf-8", -1, kindJSON},
		{"event stream", http.StatusOK, "text/event-stream", -1, kindSSE},
		{"event stream upper case", http.StatusOK, "Text/Event-Stream; charset=UTF-8", -1, kindSSE},
		{"html", http.StatusOK, "text/html", -1, kindUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.status, Header: http.Header{}, ContentLength: tt.contentLength}
			if tt.contentType != "" {
				resp.Header.Set("Content-Type", tt.contentType)
			}
			if got := classifyResponse(resp); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNegotiateChallenge(t *testing.T) {
	tests := []struct {
		name      string
		values    []string
		wantFound bool
		wantToken string
	}{
		{"none", nil, false, ""},
		{"basic only", []string{`Basic realm="example"`}, false, ""},
		{"bare negotiate", []string{"Negotiate", `Basic realm="example"`}, true, ""},
		{"combined header", []string{`Basic realm="example", negotiate`}, true, ""},
		{"with token", []string{"Negotiate dG9rZW4="}, true, "token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range tt.values {
				header.Add("WWW-Authenticate", value)
			}
			found, token, err := negotiateChallenge(header)
			if err != nil || found != tt.wantFound || string(token) != tt.wantToken {
				t.Errorf("got (%v, %q, %v), want (%v, %q, nil)", found, token, err, tt.wantFound, tt.wantToken)
			}
		})
	}
}

func TestUpstreamURL(t *testing.T) {
	for _, raw := range []string{"", "   ", "/relative/mcp", "ftp://host/mcp", "https://", "host:443/mcp"} {
		if _, err := upstreamURL(raw); err == nil {
			t.Errorf("upstreamURL(%q) accepted an invalid URL", raw)
		}
	}
	target, err := upstreamURL(" https://mcp.example.internal:8443/mcp?client=1 ")
	if err != nil || target.Hostname() != "mcp.example.internal" {
		t.Errorf("got (%v, %v)", target, err)
	}
}
