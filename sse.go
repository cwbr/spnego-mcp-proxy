package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
)

type responseKind int

const (
	kindEmpty responseKind = iota
	kindJSON
	kindSSE
	kindUnknown
)

// classifyResponse decides how a successful upstream response is forwarded.
func classifyResponse(resp *http.Response) responseKind {
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent || resp.ContentLength == 0 {
		return kindEmpty
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		return kindJSON
	case "text/event-stream":
		return kindSSE
	default:
		return kindUnknown
	}
}

// readSSE parses a server-sent event stream and calls handle for every event
// as soon as it is complete. Data lines of one event are joined with "\n".
// An event that is not terminated by a blank line at the end of the stream
// is still delivered.
func readSSE(r io.Reader, handle func(event string, data []byte)) error {
	reader := bufio.NewReader(r)
	var event string
	var data bytes.Buffer
	hasData := false

	dispatch := func() {
		if hasData {
			handle(event, bytes.Clone(data.Bytes()))
		}
		event = ""
		data.Reset()
		hasData = false
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
			if len(line) == 0 {
				dispatch()
			} else if line[0] != ':' {
				field, value, _ := bytes.Cut(line, []byte(":"))
				value = bytes.TrimPrefix(value, []byte(" "))
				switch string(field) {
				case "data":
					if hasData {
						data.WriteByte('\n')
					}
					data.Write(value)
					hasData = true
				case "event":
					event = string(value)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			dispatch()
			return nil
		}
		if err != nil {
			return err
		}
	}
}
