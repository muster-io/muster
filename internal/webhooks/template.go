// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/template/parse"
	"time"

	"github.com/theory/jsonpath"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/templates"
)

// The requests of the template mode (C-15.FR-3), and the request of the events mode as the logs name it.
const (
	RequestCreate        = "create"
	RequestUpdate        = "update"
	RequestOpenThread    = "open_thread"
	RequestReplyInThread = "reply_in_thread"
	RequestEvents        = "events"
)

// The limits of the request templates of the template mode: the extraction rules of a request, the length of a body
// template, and the longest value an extraction rule stores.
const (
	maxRules       = 10
	maxBodyLength  = 16 << 10
	maxValueLength = 1024
)

// methods are the methods of a request template (RequestTemplate.method).
var methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// The headers Muster sets on every request of the template mode, which a header template may not name; unlike the
// events mode, a template sets its own Content-Type, since only it knows the format of its body.
var reservedTemplateHeaders = []string{"content-length", "host", "webhook-id", "webhook-timestamp",
	"webhook-signature"}

// ExtractionRule picks a value from the JSON response of "create" or "open thread" (ExtractionRule): stored by Name
// per Alert Group and Destination and read by later requests as `.Response.<name>`; Path is a JSONPath (RFC 9535)
// such as `$.data.id`.
type ExtractionRule struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// RequestTemplate is one request of the template mode (RequestTemplate): its method, and its URL, headers and body,
// Go templates, with the rules that extract values from its response.
type RequestTemplate struct {
	Method  string           `json:"method"`
	URL     string           `json:"url"`
	Headers []Header         `json:"headers"`
	Body    *string          `json:"body"`
	Extract []ExtractionRule `json:"extract"`
}

// TemplateConfig is the requests of the template mode as stored in webhook_template_config (WebhookTemplateConfig):
// "create" and "update", and the optional "open thread" and "reply in thread".
type TemplateConfig struct {
	Create        RequestTemplate  `json:"create"`
	Update        RequestTemplate  `json:"update"`
	OpenThread    *RequestTemplate `json:"open_thread"`
	ReplyInThread *RequestTemplate `json:"reply_in_thread"`
}

// ParseTemplateConfig reads the stored requests of the template mode.
func ParseTemplateConfig(raw []byte) (TemplateConfig, error) {
	var c TemplateConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return TemplateConfig{}, fmt.Errorf("read the request templates: %w", err)
	}
	c.normalize()
	return c, nil
}

// JSON is the requests as stored, with empty lists rather than null.
func (c TemplateConfig) JSON() []byte {
	c.normalize()
	b, _ := json.Marshal(c) // strings only
	return b
}

func (c *TemplateConfig) normalize() {
	for _, r := range c.all() {
		if r.req.Headers == nil {
			r.req.Headers = []Header{}
		}
		if r.req.Extract == nil {
			r.req.Extract = []ExtractionRule{}
		}
	}
}

// namedRequest is a request of the template mode with its name.
type namedRequest struct {
	name string
	req  *RequestTemplate
}

// all are the requests of c that are set, in the order they are checked.
func (c *TemplateConfig) all() []namedRequest {
	out := []namedRequest{{RequestCreate, &c.Create}, {RequestUpdate, &c.Update}}
	if c.OpenThread != nil {
		out = append(out, namedRequest{RequestOpenThread, c.OpenThread})
	}
	if c.ReplyInThread != nil {
		out = append(out, namedRequest{RequestReplyInThread, c.ReplyInThread})
	}
	return out
}

// request is the request name of c, nil when it is not set.
func (c *TemplateConfig) request(name string) *RequestTemplate {
	for _, r := range c.all() {
		if r.name == name {
			return r.req
		}
	}
	return nil
}

// readable are the names of the values a request may read as `.Response.<name>`: none for "create", which runs first,
// those of "create" for "open thread" and "update" — which runs before any thread exists — and those of "create" and
// "open thread" for "reply in thread".
func (c *TemplateConfig) readable(name string) []string {
	var out []string
	if name == RequestCreate {
		return out
	}
	for _, r := range c.Create.Extract {
		out = append(out, r.Name)
	}
	if name == RequestReplyInThread && c.OpenThread != nil {
		for _, r := range c.OpenThread.Extract {
			out = append(out, r.Name)
		}
	}
	return out
}

// Mention is a resolved Mention as the request templates read it (`.Mentions`, C-15.FR-11): everyone with the choice
// of the Mention settings — all, channel or here — a group by its name, or a User by public_id, display name and login.
type Mention struct {
	Type     string
	ID       string
	Name     string
	Login    string
	Everyone string
}

// mentionsOf are the targets of a Loud call as the request templates read them.
func mentionsOf(ts []mentions.Target) []Mention {
	out := []Mention{}
	for _, t := range ts {
		switch t.Kind {
		case mentions.TargetEveryone:
			out = append(out, Mention{Type: mentions.TargetEveryone, Everyone: cmp.Or(t.Everyone, mentions.EveryoneAll)})
		case mentions.TargetGroup:
			out = append(out, Mention{Type: mentions.TargetGroup, Name: t.Group})
		case mentions.TargetUser:
			if t.User != nil {
				out = append(out, Mention{Type: mentions.TargetUser, ID: t.User.PublicID, Name: t.User.Name,
					Login: t.User.Login})
			}
		}
	}
	return out
}

// mentionText is `mention` in a request template: a target of `.Mentions` as plain text — @all, @channel or @here as
// the Mention settings chose, the group's name, or @ and the User's login — for the template to wrap in the
// receiver's syntax (C-15.FR-11).
func mentionText(target any) (string, error) {
	m, ok := target.(Mention)
	if !ok {
		return "", errors.New("mention takes a target of .Mentions")
	}
	switch m.Type {
	case mentions.TargetEveryone:
		return "@" + cmp.Or(m.Everyone, mentions.EveryoneAll), nil
	case mentions.TargetGroup:
		return m.Name, nil
	case mentions.TargetUser:
		return "@" + m.Login, nil
	}
	return "", fmt.Errorf("mention: unknown target %q", m.Type)
}

// RequestData is what the request templates of the template mode read (C-15.FR-3, FR-11): Alertmanager's template
// data and the Alert Group (the data of the Root message templates, alert data unescaped, since the receiver's format
// is unknown), or for a Storm summary the Storm and no Alert Group; the values extracted from earlier responses; the
// lifecycle event a Thread reply carries; whether the message is Loud; its Mention targets; the Destination's Secrets;
// and the text of the final edit, empty for another request.
type RequestData struct {
	Status            string
	Alerts            templates.Alerts
	GroupLabels       templates.KV
	CommonLabels      templates.KV
	CommonAnnotations templates.KV
	ExternalURL       string
	AlertGroup        *templates.AlertGroup
	Storm             *delivery.StormState
	Response          map[string]string
	Event             string
	Notify            bool
	Mentions          []Mention
	Secrets           SecretValues `json:"-"`
	Final             string
}

// SecretValues are the Destination's Secrets as the request templates read them, by name; printed or encoded whole, as
// {{ toJson . }} or {{ printf "%v" . }} would, they show as [redacted], so that only a Secret read by its name
// reaches a request.
type SecretValues map[string]string

func (SecretValues) String() string { return "[redacted]" }

// MarshalJSON hides the values.
func (SecretValues) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// dataOf is the data of the Desired state st, with the rest empty.
func dataOf(st delivery.RequestState) RequestData {
	d := RequestData{Storm: st.Storm, Response: map[string]string{}, Mentions: []Mention{},
		Secrets: SecretValues{}, Alerts: templates.Alerts{}, GroupLabels: templates.KV{},
		CommonLabels: templates.KV{}, CommonAnnotations: templates.KV{}}
	if g := st.Group; g != nil {
		d.Status, d.Alerts, d.GroupLabels, d.CommonLabels = g.Status, g.Alerts, g.GroupLabels, g.CommonLabels
		d.CommonAnnotations, d.ExternalURL = g.CommonAnnotations, g.ExternalURL
		ag := g.AlertGroup
		d.AlertGroup = &ag
	}
	return d
}

// The fields of the data whose keys a template reads by name: `.Secrets.<name>` and `.Response.<name>`.
const (
	fieldSecrets  = "Secrets"
	fieldResponse = "Response"
)

// errBareReference is a template that reads the Secrets or the extracted values other than by a literal name.
var errBareReference = errors.New("bare reference")

// refsIn are the names src reads of the map field — as .F.<name>, $.F.<name> or index .F "<name>" (or $.F) — sorted
// and without duplicates, and the position of the first other use of the field, which the request templates refuse:
// {{ with .Secrets }}, {{ $s := .Secrets }}, {{ index .Secrets $name }} or {{ $x.Secrets }} would read a name Muster
// cannot check before the request is sent (D291), so that a missing Secret would render empty. A template that does
// not parse reads none.
func refsIn(src, field string) ([]string, int, error) {
	trees, err := parseSkipping(src)
	if err != nil {
		return nil, 0, nil //nolint:nilerr // a template that does not parse is reported by the sandbox
	}
	var names []string
	bare := -1
	for _, name := range slices.Sorted(maps.Keys(trees)) {
		t := trees[name]
		if t.Root == nil {
			continue
		}
		allowed := map[parse.Node]bool{}
		walkAll(t.Root, func(n parse.Node) {
			if c, ok := n.(*parse.CommandNode); ok && len(c.Args) == 3 {
				id, isIdent := c.Args[0].(*parse.IdentifierNode)
				key, isString := c.Args[2].(*parse.StringNode)
				if isIdent && id.Ident == "index" && isString && isField(c.Args[1], field) {
					allowed[c.Args[1]] = true
					names = append(names, key.Text)
				}
			}
		})
		walkAll(t.Root, func(n parse.Node) {
			switch x := n.(type) {
			case *parse.FieldNode:
				switch {
				case len(x.Ident) >= 2 && x.Ident[0] == field:
					names = append(names, x.Ident[1])
				case len(x.Ident) == 1 && x.Ident[0] == field && !allowed[n]:
					bare = firstPos(bare, int(x.Pos))
				}
			case *parse.ChainNode:
				// (…).Secrets.x reads a field of whatever the parentheses give: it cannot be checked.
				if len(x.Field) > 0 && x.Field[0] == field {
					bare = firstPos(bare, int(x.Pos))
				}
			case *parse.VariableNode:
				switch {
				case len(x.Ident) >= 3 && x.Ident[0] == "$" && x.Ident[1] == field:
					names = append(names, x.Ident[2])
				case len(x.Ident) == 2 && x.Ident[0] == "$" && x.Ident[1] == field && !allowed[n]:
					bare = firstPos(bare, int(x.Pos))
				case len(x.Ident) >= 2 && x.Ident[0] != "$" && x.Ident[1] == field:
					bare = firstPos(bare, int(x.Pos))
				}
			}
		})
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if bare >= 0 {
		return names, bare, errBareReference
	}
	return names, 0, nil
}

func firstPos(current, pos int) int {
	if current < 0 || pos < current {
		return pos
	}
	return current
}

// isField reports whether n is .F or $.F with nothing after it.
func isField(n parse.Node, field string) bool {
	switch x := n.(type) {
	case *parse.FieldNode:
		return len(x.Ident) == 1 && x.Ident[0] == field
	case *parse.VariableNode:
		return len(x.Ident) == 2 && x.Ident[0] == "$" && x.Ident[1] == field
	}
	return false
}

// walkAll calls f on n and every node below it, the parts of a chain, a branch and a template call included.
func walkAll(n parse.Node, f func(parse.Node)) {
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
			walkAll(c, f)
		}
	case *parse.ActionNode:
		walkAll(x.Pipe, f)
	case *parse.PipeNode:
		if x == nil {
			return
		}
		for _, d := range x.Decl {
			walkAll(d, f)
		}
		for _, c := range x.Cmds {
			walkAll(c, f)
		}
	case *parse.CommandNode:
		for _, a := range x.Args {
			walkAll(a, f)
		}
	case *parse.ChainNode:
		walkAll(x.Node, f)
	case *parse.IfNode:
		walkAll(x.Pipe, f)
		walkAll(x.List, f)
		walkAll(x.ElseList, f)
	case *parse.RangeNode:
		walkAll(x.Pipe, f)
		walkAll(x.List, f)
		walkAll(x.ElseList, f)
	case *parse.WithNode:
		walkAll(x.Pipe, f)
		walkAll(x.List, f)
		walkAll(x.ElseList, f)
	case *parse.TemplateNode:
		walkAll(x.Pipe, f)
	}
}

// bareError is the FieldError of a template at pointer that reads the Secrets or the extracted values other than by
// a literal name, at the position pos of src.
func bareError(pointer, src string, pos int) *FieldError {
	line, column := position(src, pos)
	return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Line: line, Column: column,
		Detail: "Read a Secret as {{ .Secrets.<name> }} and an extracted value as {{ .Response.<name> }} (or with " +
			"$. or index and a quoted name); other uses of .Secrets and .Response are refused, since Muster could " +
			"not check that the value exists."}
}

// position is the 1-based line and column of the byte offset pos of src.
func position(src string, pos int) (int, int) {
	pos = min(max(pos, 0), len(src))
	before := src[:pos]
	line := strings.Count(before, "\n") + 1
	column := pos - strings.LastIndex(before, "\n")
	return line, column
}

// checkRefs refuses, at pointer, a template that reads the Secrets or the extracted values other than by a literal
// name, and returns the names it reads of each.
func checkRefs(pointer, src string) (secrets, response []string, err error) {
	secrets, pos, err := refsIn(src, fieldSecrets)
	if err != nil {
		return nil, nil, bareError(pointer, src, pos)
	}
	response, pos, err = refsIn(src, fieldResponse)
	if err != nil {
		return nil, nil, bareError(pointer, src, pos)
	}
	return secrets, response, nil
}

// built is a rendered request: its method, URL, headers in their order and body.
type built struct {
	method string
	url    string
	header [][2]string
	body   []byte
}

// renderer renders the request templates of one Destination for one call: Secrets and Response hold the values the
// data does not, for a dry run, a preview or the hash of a Desired state, which fill every name a template reads.
type renderer struct {
	sandbox *templates.Sandbox
	fill    func(names []string, field string) map[string]string
}

// field renders one template of a request at name (such as create/url) with the data d. A template that reads a Secret
// the Destination does not have, or a value no response gave, fails before it runs, naming it.
func (r renderer) field(name, src string, d RequestData) (string, error) {
	secrets, response, err := checkRefs(name, src)
	if err != nil {
		return "", err
	}
	if r.fill != nil {
		d.Secrets, d.Response = r.fill(secrets, fieldSecrets), r.fill(response, fieldResponse)
	}
	for _, s := range secrets {
		if _, ok := d.Secrets[s]; !ok {
			return "", fmt.Errorf("%s: the Secret %s is not set", name, s)
		}
	}
	for _, v := range response {
		if _, ok := d.Response[v]; !ok {
			return "", fmt.Errorf("%s: the value %s is missing: no response gave it", name, v)
		}
	}
	t, err := r.sandbox.Parse(name, src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	out, err := t.ExecuteIn(templates.Env{Mention: mentionText}, d)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// request renders the request rt named name with the data d: the URL must then be an absolute http or https URL, and
// neither it nor a header value may hold a line break or a NUL.
func (r renderer) request(name string, rt RequestTemplate, d RequestData) (built, error) {
	u, err := r.field(name+"/url", rt.URL, d)
	if err != nil {
		return built{}, err
	}
	u = strings.TrimSpace(u)
	if strings.ContainsAny(u, "\r\n\x00") || !validURL(u) {
		return built{}, errors.New(name + "/url: the URL is not an absolute http or https URL")
	}
	b := built{method: rt.Method, url: u}
	for i, h := range rt.Headers {
		at := name + "/headers/" + strconv.Itoa(i)
		v, err := r.field(at, h.Value, d)
		if err != nil {
			return built{}, err
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return built{}, errors.New(at + ": the header value contains a line break or a NUL")
		}
		b.header = append(b.header, [2]string{h.Name, v})
	}
	if rt.Body != nil {
		body, err := r.field(name+"/body", *rt.Body, d)
		if err != nil {
			return built{}, err
		}
		b.body = []byte(body)
	}
	return b, nil
}

// examples fills every name with example-<name>, as the dry run on save does.
func examples(names []string, _ string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = "example-" + n
	}
	return out
}

// masked fills every Secret with [redacted] and every extracted value with example-<name>, as a preview shows them.
func masked(names []string, field string) map[string]string {
	out := examples(names, field)
	if field == fieldSecrets {
		for n := range out {
			out[n] = "[redacted]"
		}
	}
	return out
}

// exampleData is the built-in example of a dry run on save: an outgoing webhook belongs to no Route, so its templates
// run against the example of C-12.FR-5 — two firing Alerts of one rule in a production cluster — as a Loud message
// with a Mention of each kind and, for a Thread reply, new Alerts.
func exampleData(now time.Time, request string) RequestData {
	alert := func(pod string) templates.Alert {
		return templates.Alert{Status: templates.StatusFiring,
			Labels: templates.KV{"alertname": "HighErrorRate", "cluster": "prod", "namespace": "shop", "pod": pod,
				"severity": "critical"},
			Annotations: templates.KV{"summary": "Error rate above 5%",
				"description": "The checkout service answers 5% of requests with errors."},
			StartsAt: now.Add(-10 * time.Minute), GeneratorURL: "https://prometheus.example.org/graph",
			Fingerprint: "example-" + pod}
	}
	d := RequestData{Status: templates.StatusFiring,
		Alerts:            templates.Alerts{alert("checkout-1"), alert("checkout-2")},
		GroupLabels:       templates.KV{"alertname": "HighErrorRate"},
		CommonLabels:      templates.KV{"alertname": "HighErrorRate", "cluster": "prod", "namespace": "shop"},
		CommonAnnotations: templates.KV{"summary": "Error rate above 5%"},
		ExternalURL:       "https://muster.example.org",
		AlertGroup: &templates.AlertGroup{Number: 1, Title: "HighErrorRate", Summary: "Error rate above 5%",
			Status: templates.StatusFiring, Route: "Default", SeverityLevel: "critical", StartedAt: now.Add(-10 * time.Minute),
			URL: "https://muster.example.org/alert-groups/AGEXAMPLE"},
		Notify: true,
		Mentions: []Mention{{Type: mentions.TargetEveryone, Everyone: mentions.EveryoneAll},
			{Type: mentions.TargetGroup, Name: "oncall"},
			{Type: mentions.TargetUser, ID: "USEXAMPLE", Name: "Alice", Login: "alice"}},
		Response: map[string]string{}, Secrets: map[string]string{}}
	if request == RequestReplyInThread || request == RequestOpenThread {
		d.Event = "alerts_added"
	}
	return d
}

// ValidateTemplate checks the requests of the template mode on save (C-15.FR-1, FR-3): "create" and "update" are set,
// each request has a method of RequestTemplate, a URL, at most maxHeaders headers with valid names that Muster does
// not set itself, and extraction rules only on "create" and "open thread", each with a template identifier as its
// name, unique among them, and a valid JSONPath; every template parses and runs on a dry run with the example, its
// Secrets and the values of the extraction rules it may read filled with example-<name>, after which the URL is an
// absolute http or https URL. The fields are trimmed of surrounding space; base is the JSON Pointer of the requests in
// the body, such as /template, and now the business time the example's Alerts started ten minutes before.
func ValidateTemplate(s *templates.Sandbox, base string, c *TemplateConfig, now time.Time) error {
	if c == nil {
		return &FieldError{Pointer: base, Code: CodeRequired, Detail: "The modes template and both need their requests."}
	}
	c.normalize()
	seen := map[string]bool{}
	for _, nr := range c.all() {
		pointer := base + "/" + nr.name
		if err := validateRules(pointer, nr.name, nr.req.Extract, seen); err != nil {
			return err
		}
	}
	for _, nr := range c.all() {
		if err := validateRequest(s, base+"/"+nr.name, nr.name, nr.req, c.readable(nr.name), now); err != nil {
			return err
		}
	}
	return nil
}

// validateRules checks the extraction rules of the request name at pointer; seen are the names taken by the rules
// checked before.
func validateRules(pointer, name string, rules []ExtractionRule, seen map[string]bool) error {
	if len(rules) > 0 && name != RequestCreate && name != RequestOpenThread {
		return &FieldError{Pointer: pointer + "/extract", Code: CodeInvalidFormat,
			Detail: "Only the requests create and open_thread extract values."}
	}
	if len(rules) > maxRules {
		return &FieldError{Pointer: pointer + "/extract", Code: CodeTooLong,
			Detail: fmt.Sprintf("A request has at most %d extraction rules.", maxRules)}
	}
	for i := range rules {
		r := &rules[i]
		at := pointer + "/extract/" + strconv.Itoa(i)
		r.Name, r.Path = strings.TrimSpace(r.Name), strings.TrimSpace(r.Path)
		switch {
		case !ValidSecretName(r.Name):
			return &FieldError{Pointer: at + "/name", Code: CodeInvalidFormat,
				Detail: "A value name is a letter or an underscore followed by letters, digits and underscores."}
		case seen[r.Name]:
			return &FieldError{Pointer: at + "/name", Code: CodeInvalidFormat,
				Detail: "Another extraction rule has this name."}
		case r.Path == "":
			return &FieldError{Pointer: at + "/path", Code: CodeRequired, Detail: "The JSONPath is empty."}
		case len(r.Path) > maxTemplateLength:
			return &FieldError{Pointer: at + "/path", Code: CodeTooLong,
				Detail: fmt.Sprintf("The JSONPath is longer than %d bytes.", maxTemplateLength)}
		}
		if _, err := jsonpath.Parse(r.Path); err != nil {
			return &FieldError{Pointer: at + "/path", Code: CodeInvalidFormat,
				Detail: "The JSONPath is not valid: " + err.Error()}
		}
		seen[r.Name] = true
	}
	return nil
}

// validateRequest checks one request at pointer; readable are the extracted values it may read.
func validateRequest(s *templates.Sandbox, pointer, name string, rt *RequestTemplate, readable []string,
	now time.Time) error {
	rt.Method = strings.ToUpper(strings.TrimSpace(rt.Method))
	rt.URL = strings.TrimSpace(rt.URL)
	switch {
	case !slices.Contains(methods, rt.Method):
		return &FieldError{Pointer: pointer + "/method", Code: CodeInvalidFormat,
			Detail: "The method is GET, POST, PUT, PATCH or DELETE."}
	case rt.URL == "":
		return &FieldError{Pointer: pointer + "/url", Code: CodeRequired, Detail: "The URL is empty."}
	case len(rt.URL) > maxTemplateLength:
		return &FieldError{Pointer: pointer + "/url", Code: CodeTooLong,
			Detail: fmt.Sprintf("The template is longer than %d bytes.", maxTemplateLength)}
	case len(rt.Headers) > maxHeaders:
		return &FieldError{Pointer: pointer + "/headers", Code: CodeTooLong,
			Detail: fmt.Sprintf("A request has at most %d headers.", maxHeaders)}
	case rt.Body != nil && len(*rt.Body) > maxBodyLength:
		return &FieldError{Pointer: pointer + "/body", Code: CodeTooLong,
			Detail: fmt.Sprintf("The template is longer than %d bytes.", maxBodyLength)}
	}
	d := exampleData(now, name)
	d.Response = examples(readable, fieldResponse)
	run := func(at, src string) (string, error) {
		secrets, response, err := checkRefs(at, src)
		if err != nil {
			return "", err
		}
		for _, v := range response {
			if _, ok := d.Response[v]; !ok {
				return "", &FieldError{Pointer: at, Code: CodeInvalidFormat,
					Detail: fmt.Sprintf("No extraction rule this request may read gives the value %s.", v)}
			}
		}
		data := d
		data.Secrets = examples(secrets, fieldSecrets)
		t, err := s.Parse(at, src)
		if err != nil {
			return "", templateError(at, err)
		}
		out, err := t.ExecuteIn(templates.Env{Mention: mentionText}, data)
		if err != nil {
			return "", templateError(at, err)
		}
		return out, nil
	}
	u, err := run(pointer+"/url", rt.URL)
	if err != nil {
		return err
	}
	if !validURL(strings.TrimSpace(u)) {
		return &FieldError{Pointer: pointer + "/url", Code: CodeInvalidFormat,
			Detail: "The URL is not an absolute http or https URL."}
	}
	headers := map[string]bool{}
	for i := range rt.Headers {
		h := &rt.Headers[i]
		at := pointer + "/headers/" + strconv.Itoa(i)
		h.Name = strings.TrimSpace(h.Name)
		lower := strings.ToLower(h.Name)
		switch {
		case h.Name == "":
			return &FieldError{Pointer: at + "/name", Code: CodeRequired, Detail: "The header name is empty."}
		case !validHeaderName(h.Name):
			return &FieldError{Pointer: at + "/name", Code: CodeInvalidFormat,
				Detail: "The header name is not a valid HTTP header name."}
		case slices.Contains(reservedTemplateHeaders, lower):
			return &FieldError{Pointer: at + "/name", Code: CodeReserved, Detail: "Muster sets this header itself."}
		case headers[lower]:
			return &FieldError{Pointer: at + "/name", Code: CodeInvalidFormat, Detail: "Another header has this name."}
		case len(h.Value) > maxTemplateLength:
			return &FieldError{Pointer: at + "/value", Code: CodeTooLong,
				Detail: fmt.Sprintf("The template is longer than %d bytes.", maxTemplateLength)}
		}
		headers[lower] = true
		v, err := run(at+"/value", h.Value)
		if err != nil {
			return err
		}
		if strings.ContainsAny(v, "\r\n") {
			return &FieldError{Pointer: at + "/value", Code: CodeInvalidFormat,
				Detail: "The header value contains a line break."}
		}
	}
	if rt.Body != nil {
		if _, err := run(pointer+"/body", *rt.Body); err != nil {
			return err
		}
	}
	return nil
}

// TemplateWarnings are the literal_credential warnings of the requests of the template mode (C-15.FR-10), as Warnings
// gives them for the events mode; base is the JSON Pointer of the requests, such as /template.
func TemplateWarnings(base string, c TemplateConfig) []Warning {
	var out []Warning
	for _, nr := range c.all() {
		out = append(out, Warnings(base+"/"+nr.name, EventsConfig{URL: nr.req.URL, Headers: nr.req.Headers})...)
	}
	return out
}

// validURL reports whether a rendered URL is an absolute http or https URL.
func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
