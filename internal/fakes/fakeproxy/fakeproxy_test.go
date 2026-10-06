// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeproxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
)

// echo accepts connections and echoes what it reads; it returns its address.
func echo(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func start(t *testing.T, kind Kind, opts Options) *Server {
	t.Helper()
	s, err := Start(t.Context(), kind, "127.0.0.1:0", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func dial(t *testing.T, s *Server) net.Conn {
	t.Helper()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func read(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func socksConnect(t *testing.T, c net.Conn, atyp byte, addr []byte, port uint16) byte {
	t.Helper()
	req := append([]byte{socksVersion, cmdConnect, 0, atyp}, addr...)
	req = binary.BigEndian.AppendUint16(req, port)
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	return read(t, c, 10)[1]
}

func parsePort(t *testing.T, s string) uint16 {
	t.Helper()
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(n)
}

// readResponse reads an answer of the proxy and closes its body.
func readResponse(t *testing.T, r *bufio.Reader) int {
	t.Helper()
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func roundTrip(t *testing.T, c net.Conn) {
	t.Helper()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if got := string(read(t, c, 4)); got != "ping" {
		t.Fatalf("echo %q", got)
	}
}

func TestStartRefusesUnknownKind(t *testing.T) {
	if _, err := Start(t.Context(), "ftp", "127.0.0.1:0", Options{}); err == nil {
		t.Fatal("started")
	}
	if _, err := Start(t.Context(), HTTP, "256.0.0.1:0", Options{}); err == nil {
		t.Fatal("listened on a bad address")
	}
}

func TestSOCKS5(t *testing.T) {
	target := echo(t)
	host, portText, _ := net.SplitHostPort(target)
	port := parsePort(t, portText)
	s := start(t, SOCKS5, Options{Username: "u", Password: "p", Hosts: map[string]string{"only.proxy": host}})

	// Domain names resolve through Hosts, with the port of the request.
	c := dial(t, s)
	_, _ = c.Write([]byte{socksVersion, 1, authPassword})
	if got := read(t, c, 2); got[1] != authPassword {
		t.Fatalf("method %v", got)
	}
	_, _ = c.Write([]byte{1, 1, 'u', 1, 'p'})
	if got := read(t, c, 2); got[1] != repSucceeded {
		t.Fatalf("auth %v", got)
	}
	if rep := socksConnect(t, c, atypDomain, append([]byte{byte(len("only.proxy"))}, "only.proxy"...), port); rep != repSucceeded {
		t.Fatalf("connect %d", rep)
	}
	roundTrip(t, c)
	if got := s.Targets(); len(got) != 1 || got[0] != net.JoinHostPort("only.proxy", portText) {
		t.Fatalf("targets %v", got)
	}

	// A wrong password and a missing method are refused and counted.
	c = dial(t, s)
	_, _ = c.Write([]byte{socksVersion, 1, authPassword, 1, 1, 'u', 1, 'x'})
	if got := read(t, c, 4); got[3] != repFailure {
		t.Fatalf("wrong password %v", got)
	}
	c = dial(t, s)
	_, _ = c.Write([]byte{socksVersion, 1, authNone})
	if got := read(t, c, 2); got[1] != authNoneOK {
		t.Fatalf("no method %v", got)
	}
	if s.Refused() != 2 {
		t.Fatalf("refused %d", s.Refused())
	}
}

func TestSOCKS5Addresses(t *testing.T) {
	target := echo(t)
	_, portText, _ := net.SplitHostPort(target)
	port := parsePort(t, portText)
	s := start(t, SOCKS5, Options{})
	open := func() net.Conn {
		c := dial(t, s)
		_, _ = c.Write([]byte{socksVersion, 1, authNone})
		if got := read(t, c, 2); got[1] != authNone {
			t.Fatalf("method %v", got)
		}
		return c
	}
	c := open()
	if rep := socksConnect(t, c, atypIPv4, []byte{127, 0, 0, 1}, port); rep != repSucceeded {
		t.Fatalf("ipv4 %d", rep)
	}
	roundTrip(t, c)
	c = open()
	if rep := socksConnect(t, c, atypIPv6, net.IPv6loopback, 1); rep != repUnreachable {
		t.Fatalf("unreachable ipv6 %d", rep)
	}
	c = open()
	_, _ = c.Write([]byte{socksVersion, 2, 0, atypIPv4, 127, 0, 0, 1, 0, 80})
	if got := read(t, c, 10); got[1] != repCmdUnknown {
		t.Fatalf("bind %v", got)
	}
	c = open()
	_, _ = c.Write([]byte{socksVersion, cmdConnect, 0, 9})
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("answered an unknown address type")
	}
}

func TestHTTPProxy(t *testing.T) {
	target := echo(t)
	s := start(t, HTTP, Options{Username: "u", Password: "p", Hosts: map[string]string{"only.proxy": target}})
	auth := "Proxy-Authorization: Basic dTpw\r\n"

	c := dial(t, s)
	_, _ = io.WriteString(c, "CONNECT only.proxy:443 HTTP/1.1\r\nHost: only.proxy:443\r\n"+auth+"\r\n")
	r := bufio.NewReader(c)
	if status := readResponse(t, r); status != http.StatusOK {
		t.Fatalf("connect %d", status)
	}
	_, _ = c.Write([]byte("ping"))
	if got := string(read(t, r, 4)); got != "ping" {
		t.Fatalf("tunnel %q", got)
	}

	c = dial(t, s)
	_, _ = io.WriteString(c, "GET http://only.proxy/x HTTP/1.1\r\nHost: only.proxy\r\n"+auth+"\r\n")
	// The echo target sends the forwarded request back, in origin form and without the proxy's headers.
	fwd, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil || fwd.RequestURI != "/x" || fwd.Header.Get("Proxy-Authorization") != "" || !fwd.Close {
		t.Fatalf("forwarded %+v %v", fwd, err)
	}

	for _, req := range []string{
		"CONNECT 127.0.0.1:1 HTTP/1.1\r\nHost: 127.0.0.1:1\r\n" + auth + "\r\n",
		"GET http://127.0.0.1:1/ HTTP/1.1\r\nHost: 127.0.0.1:1\r\n" + auth + "\r\n",
		"GET http://other/ HTTP/1.1\r\nHost: other\r\n\r\n",
	} {
		c = dial(t, s)
		_, _ = io.WriteString(c, req)
		if status := readResponse(t, bufio.NewReader(c)); status != http.StatusBadGateway &&
			status != http.StatusProxyAuthRequired {
			t.Fatalf("%q: %d", req, status)
		}
	}
	if s.Refused() != 1 {
		t.Fatalf("refused %d", s.Refused())
	}
	targets := s.Targets()
	if len(targets) != 2 || targets[0] != "only.proxy:443" || targets[1] != "only.proxy:80" {
		t.Fatalf("targets %v", targets)
	}
	conns := s.Connections()
	if conns[0].LocalAddr == "" {
		t.Fatalf("connections %+v", conns)
	}
}

func TestCloseEndsOpenTunnels(t *testing.T) {
	target := echo(t)
	s, err := Start(context.WithoutCancel(t.Context()), HTTP, "127.0.0.1:0", Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, s)
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	if status := readResponse(t, bufio.NewReader(c)); status != http.StatusOK {
		t.Fatalf("connect %d", status)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("the tunnel is still open")
	}
}

func TestControlEndpointListsTheConnections(t *testing.T) {
	target := echo(t)
	for _, kind := range []Kind{HTTP, SOCKS5} {
		t.Run(string(kind), func(t *testing.T) {
			s := start(t, kind, Options{})
			c := dial(t, s)
			if kind == HTTP {
				if _, err := io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("CONNECT = %d", resp.StatusCode)
				}
			} else {
				host, portText, _ := net.SplitHostPort(target)
				port, _ := strconv.Atoi(portText)
				req := []byte{socksVersion, 1, authNone}
				req = append(req, socksVersion, cmdConnect, 0, atypIPv4)
				req = append(req, net.ParseIP(host).To4()...)
				req = binary.BigEndian.AppendUint16(req, uint16(port)) //nolint:gosec // G115: a port fits
				if _, err := c.Write(req); err != nil {
					t.Fatal(err)
				}
				reply := make([]byte, 12)
				if _, err := io.ReadFull(c, reply); err != nil || reply[3] != repSucceeded {
					t.Fatalf("SOCKS5 reply = %v, %v", reply, err)
				}
			}
			get := func(method string) (int, []Connection) {
				req, _ := http.NewRequestWithContext(t.Context(), method, "http://"+s.Addr()+controlPath, nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				var conns []Connection
				_ = json.NewDecoder(resp.Body).Decode(&conns)
				return resp.StatusCode, conns
			}
			status, conns := get(http.MethodGet)
			if status != http.StatusOK || len(conns) != 1 || conns[0].Target != target {
				t.Fatalf("GET %s = %d %v", controlPath, status, conns)
			}
			if status, _ := get(http.MethodDelete); status != http.StatusNoContent {
				t.Fatalf("DELETE = %d", status)
			}
			if _, conns := get(http.MethodGet); len(conns) != 0 {
				t.Fatalf("after DELETE = %v", conns)
			}
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+s.Addr()+controlPath, nil)
			req.Header.Set("Origin", "http://evil.example")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("a request with an Origin header = %d", resp.StatusCode)
			}
		})
	}
}
