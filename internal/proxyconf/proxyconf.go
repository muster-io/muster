// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package proxyconf is the shared proxy settings object (C-03.FR-19, C-02.FR-22): whether a client uses a proxy of its
// own, its type, address and username, stored as `proxy jsonb`, and its password, a Secret stored beside it with the
// helper of internal/keyring. It validates an update and turns the stored object into the proxy of an outbound client
// (S-009). OIDC settings use it first; Connections, outgoing webhook Destinations and the outgoing heartbeat reuse it.
package proxyconf

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The proxy types.
const (
	TypeHTTP   = string(outbound.ProxyHTTP)
	TypeHTTPS  = string(outbound.ProxyHTTPS)
	TypeSOCKS5 = string(outbound.ProxySOCKS5)
)

// maxUsername bounds the username, which the SOCKS5 protocol sends in one length byte.
const maxUsername = 255

// Config is the proxy object as stored in `proxy jsonb`, without its password. A disabled proxy may keep its type,
// address and username, so that switching it off and on again keeps them.
type Config struct {
	Enabled  bool    `json:"enabled"`
	Type     string  `json:"type,omitempty"`
	Address  string  `json:"address,omitempty"`
	Username *string `json:"username,omitempty"`
}

// Parse reads the stored object; an empty value is a disabled proxy.
func Parse(raw []byte) (Config, error) {
	var c Config
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("read the stored proxy: %w", err)
	}
	return c, nil
}

// JSON is the object as stored.
func (c Config) JSON() []byte {
	b, _ := json.Marshal(c) // a struct of strings and a bool always encodes
	return b
}

// Input is the proxy object of an update: an omitted type, address or username keeps the stored one, a null username
// clears it, and the password follows the rule of every Secret field.
type Input struct {
	Enabled     bool
	Type        *string
	Address     *string
	UsernameSet bool
	Username    *string
	Password    keyring.SecretInput
}

// FieldError is a field of the proxy object that is not valid, at a JSON pointer relative to the object.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// Apply returns the stored object after in. An enabled proxy needs a type and an address of the form host:port.
func (in Input) Apply(stored Config) (Config, error) {
	out := stored
	out.Enabled = in.Enabled
	if in.Type != nil {
		out.Type = *in.Type
	}
	if in.Address != nil {
		out.Address = strings.TrimSpace(*in.Address)
	}
	if in.UsernameSet {
		out.Username = nil
		if in.Username != nil && *in.Username != "" {
			u := *in.Username
			out.Username = &u
		}
	}
	return out, out.Validate()
}

// Validate checks the object: the type is http, https or socks5 and the address is host:port with a port from 1 to
// 65535, both required while the proxy is enabled, and the username fits the SOCKS5 protocol.
func (c Config) Validate() error {
	switch c.Type {
	case "":
		if c.Enabled {
			return &FieldError{Pointer: "/type", Code: "required", Detail: "An enabled proxy needs its type."}
		}
	case TypeHTTP, TypeHTTPS, TypeSOCKS5:
	default:
		return &FieldError{Pointer: "/type", Code: "invalid_format", Detail: "The type is not http, https or socks5."}
	}
	if c.Address == "" {
		if c.Enabled {
			return &FieldError{Pointer: "/address", Code: "required", Detail: "An enabled proxy needs its address."}
		}
	} else if !validAddress(c.Address) {
		return &FieldError{Pointer: "/address", Code: "invalid_format",
			Detail: "The address is not host:port with a port from 1 to 65535."}
	}
	if c.Username != nil && (len(*c.Username) > maxUsername || strings.ContainsAny(*c.Username, ":\r\n")) {
		return &FieldError{Pointer: "/username", Code: "invalid_format",
			Detail: "The username is longer than 255 bytes or contains a colon or a line break."}
	}
	return nil
}

func validAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || strings.ContainsAny(host, " /?#@") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// Outbound is the proxy of an outbound client for the stored object and its password: nil while it is disabled.
func (c Config) Outbound(password logging.Secret) *outbound.Proxy {
	if !c.Enabled {
		return nil
	}
	p := &outbound.Proxy{Type: outbound.ProxyType(c.Type), Address: c.Address, Password: password}
	if c.Username != nil {
		p.Username = *c.Username
	}
	return p
}
