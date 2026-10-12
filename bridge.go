package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	errorCodeUpstream   = -32000
	errorCodeParse      = -32700
	sessionCloseTimeout = 3 * time.Second
)

// bridge forwards newline-delimited JSON-RPC messages from stdin to the
// upstream and writes every upstream message as one line to stdout.
type bridge struct {
	upstream *upstream

	outMu sync.Mutex
	out   io.Writer
}

func newBridge(up *upstream, out io.Writer) *bridge {
	return &bridge{upstream: up, out: out}
}

// run processes stdin until it is closed or ctx is cancelled. After EOF it
// waits for pending requests; on cancellation it aborts them. In both cases
// the upstream session is closed afterwards.
func (b *bridge) run(ctx context.Context, in io.Reader) {
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(in)
		for {
			line, err := reader.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	var pending sync.WaitGroup
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case line := <-lines:
			pending.Go(func() { b.handle(ctx, line) })
		case err := <-readErr:
			if !errors.Is(err, io.EOF) {
				log.Printf("reading stdin failed: %v", err)
			}
			break loop
		}
	}
	pending.Wait()
	b.upstream.closeSession(sessionCloseTimeout)
}

// message holds what the bridge needs to know about a client message.
type message struct {
	requestIDs []json.RawMessage
	initialize bool
}

func parseMessage(line []byte) (message, error) {
	var objects []json.RawMessage
	if line[0] == '[' {
		if err := json.Unmarshal(line, &objects); err != nil {
			return message{}, err
		}
	} else {
		objects = []json.RawMessage{line}
	}

	var msg message
	for _, object := range objects {
		var fields struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(object, &fields); err != nil {
			return message{}, err
		}
		if fields.Method != "" && len(fields.ID) > 0 && string(fields.ID) != "null" {
			msg.requestIDs = append(msg.requestIDs, fields.ID)
		}
		if fields.Method == "initialize" {
			msg.initialize = true
		}
	}
	return msg, nil
}

func (b *bridge) handle(ctx context.Context, line []byte) {
	msg, err := parseMessage(line)
	if err != nil {
		log.Printf("invalid JSON-RPC message from client: %v", err)
		b.writeError(json.RawMessage("null"), errorCodeParse, "parse error: "+err.Error())
		return
	}
	unanswered := make(map[string]json.RawMessage, len(msg.requestIDs))
	for _, id := range msg.requestIDs {
		unanswered[string(id)] = id
	}

	reason := b.forward(ctx, line, msg, unanswered)
	if ctx.Err() != nil {
		return
	}
	if reason != "" {
		log.Print(reason)
	}
	if len(unanswered) == 0 {
		return
	}
	if reason == "" {
		reason = "upstream sent no response"
	}
	for _, id := range unanswered {
		b.writeError(id, errorCodeUpstream, reason)
	}
}

// forward sends one client message upstream and relays the answer. It
// removes answered request IDs from unanswered and returns a failure reason.
func (b *bridge) forward(ctx context.Context, line []byte, msg message, unanswered map[string]json.RawMessage) string {
	sessionID, _ := b.upstream.session()
	resp, err := b.upstream.do(ctx, http.MethodPost, line)
	if err != nil {
		return fmt.Sprintf("upstream request failed: %v", err)
	}
	defer resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if success && msg.initialize {
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			b.upstream.setSessionID(id)
		}
	}

	switch kind := classifyResponse(resp); {
	case !success:
		if kind == kindJSON {
			// Relay JSON-RPC errors that the server sent with the status.
			if body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err == nil {
				b.emit(body, msg, unanswered)
			}
		}
		return statusError(resp, sessionID != "")
	case kind == kindJSON:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Sprintf("reading upstream response failed: %v", err)
		}
		b.emit(body, msg, unanswered)
	case kind == kindSSE:
		err := readSSE(resp.Body, func(event string, data []byte) {
			if (event == "" || event == "message") && len(bytes.TrimSpace(data)) > 0 {
				b.emit(data, msg, unanswered)
			}
		})
		if err != nil {
			return fmt.Sprintf("upstream event stream failed: %v", err)
		}
	case kind == kindUnknown:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if len(bytes.TrimSpace(body)) > 0 {
			return fmt.Sprintf("upstream returned unexpected content type %q", resp.Header.Get("Content-Type"))
		}
	}
	return ""
}

// emit writes one upstream JSON-RPC message (object or batch) as a single line.
func (b *bridge) emit(data []byte, msg message, unanswered map[string]json.RawMessage) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		log.Printf("dropping invalid JSON from upstream: %v", err)
		return
	}
	line := compact.Bytes()

	var objects []json.RawMessage
	if line[0] == '[' {
		json.Unmarshal(line, &objects)
	} else {
		objects = []json.RawMessage{line}
	}
	for _, object := range objects {
		var fields struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result *struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if json.Unmarshal(object, &fields) != nil || fields.Method != "" {
			continue
		}
		delete(unanswered, string(fields.ID))
		if msg.initialize && fields.Result != nil && fields.Result.ProtocolVersion != "" {
			b.upstream.setProtocolVersion(fields.Result.ProtocolVersion)
		}
	}
	b.writeLine(line)
}

func (b *bridge) writeError(id json.RawMessage, code int, text string) {
	type rpcError struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	line, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", id, rpcError{code, text}})
	b.writeLine(line)
}

func (b *bridge) writeLine(line []byte) {
	b.outMu.Lock()
	defer b.outMu.Unlock()
	if _, err := b.out.Write(append(line, '\n')); err != nil {
		log.Printf("writing to stdout failed: %v", err)
	}
}
