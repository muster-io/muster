// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakeproxy is a fake HTTP proxy (CONNECT tunnels and requests in absolute form, optionally over TLS) and a
// fake SOCKS5 proxy (CONNECT, optionally with username and password authentication), for the tests of the outbound
// package and for `muster dev`. Both record every target they connect to, and resolve the names in their Hosts first,
// so that a test can name a target only the proxy can resolve. Both also answer GET /_fake/requests on their own port
// with the connections they made, so that a live check can see what went through them.
package fakeproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Kind is the protocol a fake proxy speaks.
type Kind string

const (
	HTTP   Kind = "http"
	SOCKS5 Kind = "socks5"
)

const dialTimeout = 5 * time.Second

// Options configure a fake proxy.
type Options struct {
	// Username and Password, when set, are required: Proxy-Authorization Basic for HTTP, RFC 1929 for SOCKS5.
	Username, Password string
	// Hosts maps names that only the proxy resolves to the host:port it connects to instead.
	Hosts map[string]string
	// TLS, when set, makes an HTTP proxy serve TLS, as an https proxy.
	TLS *tls.Config
}

// Connection is a connection the proxy made: the target as the client asked for it, and the local address of the
// proxy's connection to it, which is the remote address the target sees.
type Connection struct {
	Target    string `json:"target"`
	LocalAddr string `json:"local_addr"`
}

// controlPath lists the connections a proxy made; DELETE forgets them.
const controlPath = "/_fake/requests"

// Server is a running fake proxy.
type Server struct {
	// ctx bounds the proxy's connections to its targets; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	kind   Kind
	opts   Options
	ln     net.Listener
	mu     sync.Mutex
	conns  []Connection
	// refused counts the requests refused for missing or wrong credentials.
	refused int
	// open are the connections in use, which Close closes.
	open   map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Start starts a fake proxy of kind on addr, such as 127.0.0.1:0; it runs until Close or the end of ctx.
func Start(ctx context.Context, kind Kind, addr string, opts Options) (*Server, error) {
	if kind != HTTP && kind != SOCKS5 {
		return nil, errors.New("fakeproxy: the kind is http or socks5")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if opts.TLS != nil {
		ln = tls.NewListener(ln, opts.TLS)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{ctx: ctx, cancel: cancel, kind: kind, opts: opts, ln: ln, open: map[net.Conn]struct{}{}}
	s.wg.Go(s.serve)
	return s, nil
}

// Addr is the host:port the proxy listens on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Connections lists the connections the proxy made, oldest first.
func (s *Server) Connections() []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.conns)
}

// Targets lists the targets the proxy connected to, oldest first.
func (s *Server) Targets() []string {
	var out []string
	for _, c := range s.Connections() {
		out = append(out, c.Target)
	}
	return out
}

// Refused counts the requests the proxy refused for missing or wrong credentials.
func (s *Server) Refused() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// Close stops the proxy, closes its connections and waits for them to end.
func (s *Server) Close() error {
	s.cancel()
	err := s.ln.Close()
	s.mu.Lock()
	s.closed = true
	for c := range s.open {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

// track adds c to the open connections; once the proxy is closing, it closes c instead.
func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = c.Close()
		return
	}
	s.open[c] = struct{}{}
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.open, c)
	s.mu.Unlock()
	_ = c.Close()
}

func (s *Server) serve() {
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.track(c)
		conns.Go(func() {
			defer s.untrack(c)
			r := bufio.NewReader(c)
			if s.kind == SOCKS5 {
				// A SOCKS5 greeting starts with the version byte; anything else is a request for the control endpoint.
				if b, err := r.Peek(1); err == nil && b[0] != socksVersion {
					s.serveHTTP(c, r)
					return
				}
				s.serveSOCKS5(c, r)
				return
			}
			s.serveHTTP(c, r)
		})
	}
}

func (s *Server) refuse() {
	s.mu.Lock()
	s.refused++
	s.mu.Unlock()
}

// dial connects to target, through Hosts first, and records the connection.
func (s *Server) dial(target string) (net.Conn, error) {
	addr := target
	if host, port, err := net.SplitHostPort(target); err == nil {
		if mapped, ok := s.opts.Hosts[host]; ok {
			addr = mapped
			if _, _, err := net.SplitHostPort(mapped); err != nil {
				addr = net.JoinHostPort(mapped, port)
			}
		}
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(s.ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.conns = append(s.conns, Connection{Target: target, LocalAddr: conn.LocalAddr().String()})
	s.mu.Unlock()
	s.track(conn)
	return conn, nil
}

func (s *Server) serveHTTP(c net.Conn, r *bufio.Reader) {
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	if req.URL.Host == "" && req.Method != http.MethodConnect {
		s.serveControl(c, req)
		return
	}
	if s.kind != HTTP {
		return
	}
	if s.opts.Username != "" || s.opts.Password != "" {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.opts.Username+":"+s.opts.Password))
		if req.Header.Get("Proxy-Authorization") != want {
			s.refuse()
			_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
			return
		}
	}
	if req.Method == http.MethodConnect {
		target, err := s.dial(req.Host)
		if err != nil {
			_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		defer s.untrack(target)
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		pipe(c, r, target)
		return
	}
	host := req.URL.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80")
	}
	target, err := s.dial(host)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer s.untrack(target)
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.Close = true
	if err := req.Write(target); err != nil {
		return
	}
	_, _ = io.Copy(c, target)
}

// pipe copies between the client, whose buffered bytes r may hold, and the target until either side ends.
func pipe(c net.Conn, r io.Reader, target net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, r); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, target); done <- struct{}{} }()
	<-done
	_ = c.Close()
	_ = target.Close()
	<-done
}

const (
	socksVersion   = 5
	authNone       = 0x00
	authPassword   = 0x02
	authNoneOK     = 0xff
	cmdConnect     = 0x01
	atypIPv4       = 0x01
	atypDomain     = 0x03
	atypIPv6       = 0x04
	repSucceeded   = 0x00
	repFailure     = 0x01
	repUnreachable = 0x04
	repCmdUnknown  = 0x07
)

// serveControl answers a request in origin form to the proxy itself: GET /_fake/requests lists the connections it
// made and DELETE forgets them. Like the control endpoints of the fake servers, it refuses requests a browser makes.
func (s *Server) serveControl(c net.Conn, req *http.Request) {
	status, body := http.StatusNotFound, []byte(`{"error":"not found"}`)
	switch {
	case req.Header.Get("Origin") != "":
		status, body = http.StatusForbidden, []byte(`{"error":"the control endpoint refuses requests with an Origin header"}`)
	case req.URL.Path == controlPath && req.Method == http.MethodGet:
		conns := s.Connections()
		if conns == nil {
			conns = []Connection{}
		}
		status = http.StatusOK
		body, _ = json.Marshal(conns) // a slice of strings always encodes
	case req.URL.Path == controlPath && req.Method == http.MethodDelete:
		s.mu.Lock()
		s.conns = nil
		s.mu.Unlock()
		status, body = http.StatusNoContent, nil
	}
	resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Close: true,
		Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: int64(len(body)),
		Body: io.NopCloser(bytes.NewReader(body))}
	_ = resp.Write(c)
}

func (s *Server) serveSOCKS5(c net.Conn, r *bufio.Reader) {
	if !s.socksAuth(c, r) {
		return
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil || head[0] != socksVersion {
		return
	}
	host, err := readSOCKSAddr(r, head[3])
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(r, portBytes); err != nil {
		return
	}
	if head[1] != cmdConnect {
		socksReply(c, repCmdUnknown)
		return
	}
	target, err := s.dial(net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
	if err != nil {
		socksReply(c, repUnreachable)
		return
	}
	defer s.untrack(target)
	socksReply(c, repSucceeded)
	pipe(c, r, target)
}

// socksAuth negotiates the method: none without credentials, username and password (RFC 1929) with them.
func (s *Server) socksAuth(c net.Conn, r *bufio.Reader) bool {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil || head[0] != socksVersion {
		return false
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(r, methods); err != nil {
		return false
	}
	want := byte(authNone)
	if s.opts.Username != "" || s.opts.Password != "" {
		want = authPassword
	}
	if !slices.Contains(methods, want) {
		s.refuse()
		_, _ = c.Write([]byte{socksVersion, authNoneOK})
		return false
	}
	if _, err := c.Write([]byte{socksVersion, want}); err != nil {
		return false
	}
	if want == authNone {
		return true
	}
	ver, err := r.ReadByte()
	if err != nil || ver != 1 {
		return false
	}
	user, err := readShort(r)
	if err != nil {
		return false
	}
	pass, err := readShort(r)
	if err != nil {
		return false
	}
	if user != s.opts.Username || pass != s.opts.Password {
		s.refuse()
		_, _ = c.Write([]byte{1, repFailure})
		return false
	}
	_, err = c.Write([]byte{1, repSucceeded})
	return err == nil
}

func readShort(r *bufio.Reader) (string, error) {
	n, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readSOCKSAddr(r *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case atypIPv4, atypIPv6:
		size := net.IPv4len
		if atyp == atypIPv6 {
			size = net.IPv6len
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case atypDomain:
		return readShort(r)
	}
	return "", errors.New("fakeproxy: unknown address type")
}

func socksReply(c net.Conn, rep byte) {
	_, _ = c.Write([]byte{socksVersion, rep, 0, atypIPv4, 0, 0, 0, 0, 0, 0})
}
