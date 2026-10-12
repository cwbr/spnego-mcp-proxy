//go:build !windows && !darwin

package main

import (
	"errors"
	"runtime"
)

func newNegotiator() (Negotiator, error) {
	return nil, errors.New("SPNEGO is only supported on Windows and macOS, not on " + runtime.GOOS)
}
