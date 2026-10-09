// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package webhooks is the outgoing webhook (C-15): the request templates of the events mode with their Secrets, the
// Signing secret in the style of the Standard Webhooks specification, the version 1 body of an event with its Mentions
// as data, and the adapter that sends an event through internal/outbound under the outbound address policy and the
// Destination's proxy and maps the answer to a delivery outcome. Delivery queues and reconciles the events (ADR-0005);
// this package never decides when to send.
package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/templates"
)

// The modes of an outgoing webhook Destination (C-15.FR-1).
const (
	ModeEvents   = "events"
	ModeTemplate = "template"
	ModeBoth     = "both"
)

// The kinds of DestinationWarning (C-15.FR-5, FR-10).
const (
	WarningPreviousSigningSecret = "previous_signing_secret_active"
	WarningLiteralCredential     = "literal_credential" //nolint:gosec // G101: the name of a warning, not a credential
)

// The codes of FieldError.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeTooLong       = "too_long"
	CodeReserved      = "reserved"
)

// The limits of the request templates of the events mode.
const (
	maxHeaders        = 50
	maxTemplateLength = 4096
	maxSecretLength   = 8192
)

// Header is a header of a request: its name and its value, a Go template that may read `.Secrets`.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// EventsConfig is the request of the events mode as stored in webhook_events_config (WebhookEventsConfig): its URL and
// its headers, Go templates rendered with `.Secrets`.
type EventsConfig struct {
	URL     string   `json:"url"`
	Headers []Header `json:"headers"`
}

// ParseEventsConfig reads the stored request of the events mode.
func ParseEventsConfig(raw []byte) (EventsConfig, error) {
	var c EventsConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return EventsConfig{}, fmt.Errorf("read the events request: %w", err)
	}
	if c.Headers == nil {
		c.Headers = []Header{}
	}
	return c, nil
}

// JSON is the request as stored.
func (c EventsConfig) JSON() []byte {
	if c.Headers == nil {
		c.Headers = []Header{}
	}
	b, _ := json.Marshal(c) // strings only
	return b
}

// FieldError is a field of the events request or of a Secret that is not valid, at a JSON pointer of the request
// body, with the 1-based line and column of a template error when known.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	Line    int
	Column  int
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// The headers Muster sets on every events-mode request, which a header template may not name.
var reservedHeaders = []string{"content-type", "content-length", "host", "webhook-id", "webhook-timestamp",
	"webhook-signature"}

// secretName is a Secret name: a template identifier (destination_secrets_name_check).
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidSecretName reports whether name can name a Secret.
func ValidSecretName(name string) bool { return secretName.MatchString(name) }

// Validate checks the request of the events mode on save (C-15.FR-1): the URL and every header parse in the sandbox
// and run on a dry run in which every Secret they read has a placeholder value, the URL then is an absolute http or
// https URL, and every header name is a valid name that Muster does not set itself. The URL and header values are
// trimmed of surrounding space; base is the JSON Pointer of the request in the body, such as /events.
func Validate(s *templates.Sandbox, base string, c *EventsConfig) error {
	c.URL = strings.TrimSpace(c.URL)
	if c.URL == "" {
		return &FieldError{Pointer: base + "/url", Code: CodeRequired, Detail: "The URL is empty."}
	}
	if len(c.Headers) > maxHeaders {
		return &FieldError{Pointer: base + "/headers", Code: CodeTooLong,
			Detail: fmt.Sprintf("A request has at most %d headers.", maxHeaders)}
	}
	u, err := dryRun(s, base+"/url", c.URL)
	if err != nil {
		return err
	}
	if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return &FieldError{Pointer: base + "/url", Code: CodeInvalidFormat,
			Detail: "The URL is not an absolute http or https URL."}
	}
	seen := map[string]bool{}
	for i := range c.Headers {
		h := &c.Headers[i]
		pointer := base + "/headers/" + strconv.Itoa(i)
		h.Name = strings.TrimSpace(h.Name)
		lower := strings.ToLower(h.Name)
		switch {
		case h.Name == "":
			return &FieldError{Pointer: pointer + "/name", Code: CodeRequired, Detail: "The header name is empty."}
		case !validHeaderName(h.Name):
			return &FieldError{Pointer: pointer + "/name", Code: CodeInvalidFormat,
				Detail: "The header name is not a valid HTTP header name."}
		case slices.Contains(reservedHeaders, lower):
			return &FieldError{Pointer: pointer + "/name", Code: CodeReserved,
				Detail: "Muster sets this header itself."}
		case seen[lower]:
			return &FieldError{Pointer: pointer + "/name", Code: CodeInvalidFormat,
				Detail: "Another header has this name."}
		}
		seen[lower] = true
		v, err := dryRun(s, pointer+"/value", h.Value)
		if err != nil {
			return err
		}
		if strings.ContainsAny(v, "\r\n") {
			return &FieldError{Pointer: pointer + "/value", Code: CodeInvalidFormat,
				Detail: "The header value contains a line break."}
		}
	}
	return nil
}

// validHeaderName reports whether name is an HTTP token.
func validHeaderName(name string) bool {
	for _, r := range name {
		if r > 0x7e || r <= 0x20 || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, r) {
			return false
		}
	}
	return name != "" && http.CanonicalHeaderKey(name) != ""
}

// dryRun parses src and runs it with a placeholder for every Secret it reads, as Validate does.
func dryRun(s *templates.Sandbox, pointer, src string) (string, error) {
	if len(src) > maxTemplateLength {
		return "", &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("The template is longer than %d bytes.", maxTemplateLength)}
	}
	t, err := s.Parse(pointer, src)
	if err != nil {
		return "", templateError(pointer, err)
	}
	refs := SecretRefs(src)
	values := make(map[string]string, len(refs))
	for _, name := range refs {
		values[name] = "example-" + name
	}
	out, err := t.Execute(Data{Secrets: values})
	if err != nil {
		return "", templateError(pointer, err)
	}
	return out, nil
}

// templateError is the FieldError of a template that fails to parse or to run.
func templateError(pointer string, err error) *FieldError {
	fe := &FieldError{Pointer: pointer, Code: templates.CodeSyntax, Detail: err.Error()}
	var te *templates.Error
	if errors.As(err, &te) {
		fe.Code, fe.Detail, fe.Line, fe.Column = te.Code, te.Detail, te.Line, te.Column
	}
	return fe
}

// Data is what the request templates of the events mode read: the Destination's Secrets, by name.
type Data struct {
	Secrets map[string]string
}

// SecretRefs are the names of the Secrets src reads as `.Secrets.<name>`, sorted and without duplicates; a template
// that does not parse reads none.
func SecretRefs(src string) []string {
	trees, err := parseSkipping(src)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range trees {
		if t.Root == nil {
			continue
		}
		walk(t.Root, func(n parse.Node) {
			if f, ok := n.(*parse.FieldNode); ok && len(f.Ident) >= 2 && f.Ident[0] == "Secrets" {
				out = append(out, f.Ident[1])
			}
			// index .Secrets "name"
			if c, ok := n.(*parse.CommandNode); ok && len(c.Args) >= 3 {
				id, isIdent := c.Args[0].(*parse.IdentifierNode)
				f, isField := c.Args[1].(*parse.FieldNode)
				name, isString := c.Args[2].(*parse.StringNode)
				if isIdent && id.Ident == "index" && isField && len(f.Ident) == 1 && f.Ident[0] == "Secrets" && isString {
					out = append(out, name.Text)
				}
			}
		})
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// parseSkipping parses src without checking the names of the functions it calls.
func parseSkipping(src string) (map[string]*parse.Tree, error) {
	trees := map[string]*parse.Tree{}
	t := parse.New("refs")
	t.Mode = parse.SkipFuncCheck
	_, err := t.Parse(src, "", "", trees)
	return trees, err
}

// walk calls f on n and every node below it.
func walk(n parse.Node, f func(parse.Node)) {
	if n == nil {
		return
	}
	f(n)
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			walk(c, f)
		}
	case *parse.ActionNode:
		walk(x.Pipe, f)
	case *parse.PipeNode:
		if x == nil {
			return
		}
		for _, c := range x.Cmds {
			walk(c, f)
		}
	case *parse.CommandNode:
		for _, a := range x.Args {
			walk(a, f)
		}
	case *parse.IfNode:
		walk(x.Pipe, f)
		walk(x.List, f)
		walk(x.ElseList, f)
	case *parse.RangeNode:
		walk(x.Pipe, f)
		walk(x.List, f)
		walk(x.ElseList, f)
	case *parse.WithNode:
		walk(x.Pipe, f)
		walk(x.List, f)
		walk(x.ElseList, f)
	case *parse.TemplateNode:
		walk(x.Pipe, f)
	}
}

// Warning is a DestinationWarning of an outgoing webhook that its request templates give: a literal credential at the
// JSON Pointer Field.
type Warning struct {
	Kind  string
	Field string
}

// Warnings are the literal_credential warnings of the request of the events mode (C-15.FR-10): a header named
// Authorization, or a URL whose user information or query holds a literal value, that reads no Secret. base is the JSON
// Pointer of the request, such as /events.
func Warnings(base string, c EventsConfig) []Warning {
	var out []Warning
	if literalInURL(c.URL) {
		out = append(out, Warning{Kind: WarningLiteralCredential, Field: base + "/url"})
	}
	for i, h := range c.Headers {
		if strings.EqualFold(strings.TrimSpace(h.Name), "authorization") && strings.TrimSpace(h.Value) != "" &&
			len(SecretRefs(h.Value)) == 0 {
			out = append(out, Warning{Kind: WarningLiteralCredential, Field: base + "/headers/" + strconv.Itoa(i) +
				"/value"})
		}
	}
	return out
}

// literalInURL reports whether the user information or the query of the URL template holds a value that is not a
// Secret: some part of them is literal text rather than template actions that read `.Secrets`.
func literalInURL(src string) bool {
	// Each action is replaced with a marker, so that the literal text of the template is what remains.
	trees, err := parseSkipping(src)
	if err != nil {
		return false
	}
	t := trees["refs"]
	if t == nil || t.Root == nil {
		return false
	}
	var b strings.Builder
	for _, n := range t.Root.Nodes {
		if tn, ok := n.(*parse.TextNode); ok {
			b.Write(tn.Text)
			continue
		}
		readsSecret := false
		walk(n, func(m parse.Node) {
			if f, ok := m.(*parse.FieldNode); ok && len(f.Ident) >= 2 && f.Ident[0] == "Secrets" {
				readsSecret = true
			}
		})
		if readsSecret {
			b.WriteString("SECRETREF")
		} else {
			b.WriteString("x")
		}
	}
	u, err := url.Parse(b.String())
	if err != nil {
		return false
	}
	if u.User != nil {
		for _, part := range []string{u.User.Username(), password(u.User)} {
			if part != "" && part != "SECRETREF" {
				return true
			}
		}
	}
	for _, values := range u.Query() {
		for _, v := range values {
			if v != "" && v != "SECRETREF" {
				return true
			}
		}
	}
	return false
}

func password(u *url.Userinfo) string {
	p, _ := u.Password()
	return p
}

// redactor replaces the secret values of a request with [redacted] in a text, verbatim and in the escaped forms a URL
// gives them, longer values first.
type redactor []string

func newRedactor(secrets []logging.Secret) redactor {
	var forms []string
	for _, s := range secrets {
		if s == "" {
			continue
		}
		raw := string(s)
		for _, f := range []string{raw, url.PathEscape(raw), url.QueryEscape(raw)} {
			if !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return forms
}

func (r redactor) redact(s string) string {
	for _, f := range r {
		s = strings.ReplaceAll(s, f, "[redacted]")
	}
	return s
}
