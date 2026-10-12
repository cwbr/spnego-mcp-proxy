package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// Negotiator creates SPNEGO security contexts with the credentials of the
// logged-in user. The platform implementations live in negotiate_*.go.
type Negotiator interface {
	// NewContext starts a handshake for the HTTP service on host.
	NewContext(host string) (SecurityContext, error)
}

// SecurityContext runs a single SPNEGO handshake.
type SecurityContext interface {
	// Step consumes the server token (nil on the first call) and returns
	// the next client token. done reports that the client side is complete.
	Step(serverToken []byte) (clientToken []byte, done bool, err error)
	Close()
}

// negotiateChallenge reports whether the header contains a Negotiate
// challenge and returns its optional token.
func negotiateChallenge(header http.Header) (found bool, token []byte, err error) {
	for _, value := range header.Values("WWW-Authenticate") {
		for _, challenge := range strings.Split(value, ",") {
			fields := strings.Fields(challenge)
			if len(fields) == 0 || !strings.EqualFold(fields[0], "Negotiate") {
				continue
			}
			if len(fields) == 1 {
				return true, nil, nil
			}
			token, err := base64.StdEncoding.DecodeString(fields[1])
			if err != nil {
				return true, nil, fmt.Errorf("invalid Negotiate token from server: %w", err)
			}
			return true, token, nil
		}
	}
	return false, nil, nil
}
