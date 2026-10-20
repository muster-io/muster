// Package websocket is a stub of github.com/coder/websocket with the dial and accept functions that rule 4 tells
// apart, so the fixture needs no network and no real dependency.
package websocket

import (
	"context"
	"net/http"
)

type Conn struct{}

type DialOptions struct {
	HTTPClient *http.Client
	HTTPHeader http.Header
}

type AcceptOptions struct{}

func Dial(ctx context.Context, u string, opts *DialOptions) (*Conn, *http.Response, error) {
	return &Conn{}, nil, nil
}

func Accept(w http.ResponseWriter, r *http.Request, opts *AcceptOptions) (*Conn, error) {
	return &Conn{}, nil
}

func (c *Conn) CloseNow() error { return nil }
