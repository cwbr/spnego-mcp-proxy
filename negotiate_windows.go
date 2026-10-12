//go:build windows

package main

import (
	"fmt"

	"github.com/alexbrainman/sspi"
	"github.com/alexbrainman/sspi/negotiate"
)

// sspiNegotiator uses the Windows SSPI "Negotiate" package with the
// credentials of the logged-in domain user.
type sspiNegotiator struct{}

func newNegotiator() (Negotiator, error) {
	return sspiNegotiator{}, nil
}

func (sspiNegotiator) NewContext(host string) (SecurityContext, error) {
	return &sspiContext{spn: "HTTP/" + host}, nil
}

type sspiContext struct {
	spn  string
	cred *sspi.Credentials
	ctx  *negotiate.ClientContext
}

func (c *sspiContext) Step(serverToken []byte) ([]byte, bool, error) {
	if c.ctx == nil {
		cred, err := negotiate.AcquireCurrentUserCredentials()
		if err != nil {
			return nil, false, fmt.Errorf("acquire Windows credentials: %w", err)
		}
		c.cred = cred
		ctx, token, err := negotiate.NewClientContext(cred, c.spn)
		if err != nil {
			return nil, false, fmt.Errorf("create SSPI context for %s: %w", c.spn, err)
		}
		c.ctx = ctx
		return token, false, nil
	}
	done, token, err := c.ctx.Update(serverToken)
	if err != nil {
		return nil, false, fmt.Errorf("SSPI negotiation for %s: %w", c.spn, err)
	}
	return token, done, nil
}

func (c *sspiContext) Close() {
	if c.ctx != nil {
		c.ctx.Release()
	}
	if c.cred != nil {
		c.cred.Release()
	}
}
