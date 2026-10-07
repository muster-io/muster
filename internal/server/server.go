// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package server runs the three listeners of C-02.FR-3: ingest (ingestion, Heartbeat, Mattermost callbacks, the
// Telegram webhook), app (UI and API) and internal (health and metrics). When ingest and app have the same address,
// one server routes the ingest paths to the ingest handler and every other path to the app handler.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const readHeaderTimeout = 10 * time.Second

// The names of the listeners, as log lines and errors call them.
const (
	ListenerApp      = "app"
	ListenerIngest   = "ingest"
	ListenerInternal = "internal"
)

// IsIngestPath reports whether the ingest handler serves p: ingestion, the Heartbeat, and the Mattermost and Telegram
// callbacks.
func IsIngestPath(p string) bool {
	switch {
	case p == "/api/v1/ingest", strings.HasPrefix(p, "/api/v1/ingest/"),
		IsHeartbeatPath(p), strings.HasPrefix(p, "/api/v1/callbacks/"):
		return true
	}
	return false
}

// IsHeartbeatPath reports whether p is the Heartbeat endpoint, with or without a token in the path.
func IsHeartbeatPath(p string) bool {
	return p == "/api/v1/heartbeat" || strings.HasPrefix(p, "/api/v1/heartbeat/")
}

// Ingest is the handler of the ingest listener: heartbeat serves the Heartbeat endpoint and ingest everything else,
// ingestion and the messenger callbacks.
func Ingest(ingest, heartbeat http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsHeartbeatPath(r.URL.Path) {
			heartbeat.ServeHTTP(w, r)
			return
		}
		ingest.ServeHTTP(w, r)
	})
}

// Merge routes the ingest paths to ingest and every other path to app, for one port that serves both.
func Merge(app, ingest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsIngestPath(r.URL.Path) {
			ingest.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// Addresses are the listen addresses.
type Addresses struct {
	App, Ingest, Internal string
}

// Handlers serve the listeners.
type Handlers struct {
	App, Ingest, Internal http.Handler
}

// Server is the running listeners.
type Server struct {
	listeners []*listener
	errs      chan error
	wg        sync.WaitGroup
}

type listener struct {
	name string
	ln   net.Listener
	srv  *http.Server
}

// Start listens on every address, then serves; a listener that cannot listen closes the others and is an error
// naming its variable. ctx is the base context of every request.
func Start(ctx context.Context, addrs Addresses, h Handlers) (*Server, error) {
	type spec struct {
		name, variable, addr string
		handler              http.Handler
	}
	specs := []spec{{ListenerApp, "MUSTER_LISTEN_APP", addrs.App, h.App}}
	if addrs.Ingest == addrs.App {
		specs[0].handler = Merge(h.App, h.Ingest)
	} else {
		specs = append(specs, spec{ListenerIngest, "MUSTER_LISTEN_INGEST", addrs.Ingest, h.Ingest})
	}
	specs = append(specs, spec{ListenerInternal, "MUSTER_LISTEN_INTERNAL", addrs.Internal, h.Internal})

	s := &Server{errs: make(chan error, len(specs))}
	var lc net.ListenConfig
	for _, sp := range specs {
		ln, err := lc.Listen(ctx, "tcp", sp.addr)
		if err != nil {
			for _, l := range s.listeners {
				_ = l.ln.Close()
			}
			return nil, fmt.Errorf("listen on %s (%s): %w", sp.addr, sp.variable, err)
		}
		s.listeners = append(s.listeners, &listener{name: sp.name, ln: ln, srv: &http.Server{
			Handler:           sp.handler,
			ReadHeaderTimeout: readHeaderTimeout,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}})
	}
	for _, l := range s.listeners {
		s.wg.Go(func() {
			if err := l.srv.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.errs <- &ListenerError{Listener: l.name, Err: err}
			}
		})
	}
	return s, nil
}

// ListenerError is a listener that stopped serving.
type ListenerError struct {
	Listener string
	Err      error
}

func (e *ListenerError) Error() string {
	return fmt.Sprintf("the %s listener stopped: %v", e.Listener, e.Err)
}

func (e *ListenerError) Unwrap() error { return e.Err }

// Errors delivers a ListenerError for each listener that stops serving on its own.
func (s *Server) Errors() <-chan error { return s.errs }

// Addrs are the addresses the listeners are bound to; Ingest equals App when one port serves both.
func (s *Server) Addrs() Addresses {
	var a Addresses
	for _, l := range s.listeners {
		switch l.name {
		case ListenerApp:
			a.App = l.ln.Addr().String()
		case ListenerIngest:
			a.Ingest = l.ln.Addr().String()
		case ListenerInternal:
			a.Internal = l.ln.Addr().String()
		}
	}
	if a.Ingest == "" {
		a.Ingest = a.App
	}
	return a
}

// Shutdown stops accepting connections and waits for the requests in progress until ctx ends; then it closes the
// remaining connections and returns ctx's error. The app and ingest listeners drain first and the internal listener
// last, so that readiness keeps answering, with 503, and metrics stay scrapable during the drain.
func (s *Server) Shutdown(ctx context.Context) error {
	var public, internal []*listener
	for _, l := range s.listeners {
		if l.name == ListenerInternal {
			internal = append(internal, l)
		} else {
			public = append(public, l)
		}
	}
	err := errors.Join(shutdown(ctx, public), shutdown(ctx, internal))
	s.wg.Wait()
	if err != nil {
		return ctx.Err()
	}
	return nil
}

// shutdown drains listeners in parallel and closes those that do not drain before ctx ends.
func shutdown(ctx context.Context, listeners []*listener) error {
	errs := make([]error, len(listeners))
	var wg sync.WaitGroup
	for i, l := range listeners {
		wg.Go(func() {
			if err := l.srv.Shutdown(ctx); err != nil {
				_ = l.srv.Close()
				errs[i] = err
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
