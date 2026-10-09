// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package webhooks

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/templates"
)

func str(s string) *string { return &s }

// chatConfig is the request templates of a chat with two-step threads, as the documentation's recipe has them.
func chatConfig(base string) TemplateConfig {
	return TemplateConfig{
		Create: RequestTemplate{Method: "post", URL: " " + base + "/chat/ops/messages ",
			Headers: []Header{{Name: " Authorization ", Value: "Bearer {{ .Secrets.token }}"}},
			Body: str(`{"text":{{ printf "#%d %s" .AlertGroup.Number .AlertGroup.Title | toJson }},` +
				`"who":{{ range .Mentions }}{{ mention . | toJson }}{{ end }}}`),
			Extract: []ExtractionRule{{Name: " id ", Path: " $.data.id "}}},
		Update: RequestTemplate{Method: "PUT", URL: base + "/chat/ops/messages/{{ .Response.id }}",
			Body: str(`{"text":{{ .AlertGroup.Status | toJson }},"final":{{ .Final | toJson }}}`)},
		OpenThread: &RequestTemplate{Method: "POST", URL: base + "/chat/ops/threads",
			Body: str(`{"root":"{{ .Response.id }}"}`), Extract: []ExtractionRule{{Name: "thread", Path: "$.thread.id"}}},
		ReplyInThread: &RequestTemplate{Method: "POST", URL: base + "/chat/ops/threads/{{ $.Response.thread }}/messages",
			Headers: []Header{{Name: "X-Key", Value: `{{ index .Secrets "token" }}`}},
			Body:    str(`{"text":{{ printf "%s %v" .Event .Notify | toJson }}}`)},
	}
}

// TestValidateTemplate is the check of the request templates on save (C-15.FR-1, FR-3): every request parses and runs
// on a dry run with the example, its Secrets and the values it may read filled in; the fields are trimmed and the
// method upper-cased; errors name the field and, for a template, its line and column.
func TestValidateTemplate(t *testing.T) {
	sb := sandbox()
	c := chatConfig("https://chat.example.org")
	if err := ValidateTemplate(sb, "/template", &c, t0); err != nil {
		t.Fatal(err)
	}
	if c.Create.Method != http.MethodPost || c.Create.URL != "https://chat.example.org/chat/ops/messages" ||
		c.Create.Headers[0].Name != "Authorization" || c.Create.Extract[0] != (ExtractionRule{Name: "id",
		Path: "$.data.id"}) || c.Update.Headers == nil || c.Update.Extract == nil {
		t.Errorf("not normalized: %+v", c)
	}
	if err := ValidateTemplate(sb, "/template", nil, t0); err == nil {
		t.Error("no requests were accepted")
	}
	type mut func(c *TemplateConfig)
	for name, cc := range map[string]struct {
		mut     mut
		pointer string
		code    string
	}{
		"method":       {func(c *TemplateConfig) { c.Create.Method = "TRACE" }, "/template/create/method", CodeInvalidFormat},
		"no url":       {func(c *TemplateConfig) { c.Update.URL = " " }, "/template/update/url", CodeRequired},
		"long url":     {func(c *TemplateConfig) { c.Update.URL = strings.Repeat("a", maxTemplateLength+1) }, "/template/update/url", CodeTooLong},
		"not http":     {func(c *TemplateConfig) { c.Update.URL = "ftp://x" }, "/template/update/url", CodeInvalidFormat},
		"many headers": {func(c *TemplateConfig) { c.Create.Headers = make([]Header, maxHeaders+1) }, "/template/create/headers", CodeTooLong},
		"long body":    {func(c *TemplateConfig) { c.Create.Body = str(strings.Repeat("a", maxBodyLength+1)) }, "/template/create/body", CodeTooLong},
		"empty header": {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: " "}} }, "/template/create/headers/0/name", CodeRequired},
		"bad header":   {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: "a b"}} }, "/template/create/headers/0/name", CodeInvalidFormat},
		"reserved":     {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: "Webhook-Id"}} }, "/template/create/headers/0/name", CodeReserved},
		"duplicate":    {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: "A"}, {Name: "a"}} }, "/template/create/headers/1/name", CodeInvalidFormat},
		"long header": {func(c *TemplateConfig) {
			c.Create.Headers = []Header{{Name: "A", Value: strings.Repeat("a", maxTemplateLength+1)}}
		}, "/template/create/headers/0/value", CodeTooLong},
		"line break":     {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: "A", Value: "1\n2"}} }, "/template/create/headers/0/value", CodeInvalidFormat},
		"header syntax":  {func(c *TemplateConfig) { c.Create.Headers = []Header{{Name: "A", Value: "{{ end }}"}} }, "/template/create/headers/0/value", templates.CodeSyntax},
		"body runtime":   {func(c *TemplateConfig) { c.Create.Body = str("{{ len 3 }}") }, "/template/create/body", templates.CodeSyntax},
		"extract update": {func(c *TemplateConfig) { c.Update.Extract = []ExtractionRule{{Name: "x", Path: "$.x"}} }, "/template/update/extract", CodeInvalidFormat},
		"many rules":     {func(c *TemplateConfig) { c.Create.Extract = make([]ExtractionRule, maxRules+1) }, "/template/create/extract", CodeTooLong},
		"rule name":      {func(c *TemplateConfig) { c.Create.Extract[0].Name = "a-b" }, "/template/create/extract/0/name", CodeInvalidFormat},
		"same rule":      {func(c *TemplateConfig) { c.OpenThread.Extract[0].Name = "id" }, "/template/open_thread/extract/0/name", CodeInvalidFormat},
		"no path":        {func(c *TemplateConfig) { c.Create.Extract[0].Path = " " }, "/template/create/extract/0/path", CodeRequired},
		"long path":      {func(c *TemplateConfig) { c.Create.Extract[0].Path = "$." + strings.Repeat("a", maxTemplateLength) }, "/template/create/extract/0/path", CodeTooLong},
		"bad path":       {func(c *TemplateConfig) { c.Create.Extract[0].Path = "$.[" }, "/template/create/extract/0/path", CodeInvalidFormat},
		"create reads":   {func(c *TemplateConfig) { c.Create.URL = "https://x/{{ .Response.id }}" }, "/template/create/url", CodeInvalidFormat},
		"open reads own": {func(c *TemplateConfig) { c.OpenThread.URL = "https://x/{{ .Response.thread }}" }, "/template/open_thread/url", CodeInvalidFormat},
		"unknown value":  {func(c *TemplateConfig) { c.Update.Body = str("{{ .Response.nope }}") }, "/template/update/body", CodeInvalidFormat},
		"update thread":  {func(c *TemplateConfig) { c.Update.Body = str("{{ .Response.thread }}") }, "/template/update/body", CodeInvalidFormat},
		"bare secrets":   {func(c *TemplateConfig) { c.Create.Body = str("{{ with .Secrets }}{{ .token }}{{ end }}") }, "/template/create/body", CodeInvalidFormat},
		"secrets var":    {func(c *TemplateConfig) { c.Update.Body = str("{{ $s := .Secrets }}{{ $s.token }}") }, "/template/update/body", CodeInvalidFormat},
		"dynamic index":  {func(c *TemplateConfig) { c.Update.Body = str(`{{ $n := "token" }}{{ index .Secrets $n }}`) }, "/template/update/body", CodeInvalidFormat},
		"other root":     {func(c *TemplateConfig) { c.Update.Body = str("{{ $r := . }}{{ $r.Secrets.token }}") }, "/template/update/body", CodeInvalidFormat},
		"bare response":  {func(c *TemplateConfig) { c.Update.Body = str("{{ toJson .Response }}") }, "/template/update/body", CodeInvalidFormat},
		"nil storm":      {func(c *TemplateConfig) { c.Update.Body = str("{{ .Storm.Route }}") }, "/template/update/body", templates.CodeSyntax},
	} {
		c := chatConfig("https://chat.example.org")
		cc.mut(&c)
		err := ValidateTemplate(sb, "/template", &c, t0)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != cc.pointer || fe.Code != cc.code {
			t.Errorf("%s = %v", name, err)
		}
	}
	c = chatConfig("https://x")
	c.Update.Body = str("ok\n  {{ with .Secrets }}{{ end }}")
	err := ValidateTemplate(sb, "/template", &c, t0)
	if fe, ok := errors.AsType[*FieldError](err); !ok || fe.Line != 2 || fe.Column != 11 {
		t.Errorf("position of a bare reference = %+v", err)
	}
	c = chatConfig("https://x")
	c.OpenThread, c.ReplyInThread = nil, nil
	if err := ValidateTemplate(sb, "/template", &c, t0); err != nil {
		t.Errorf("one-step requests: %v", err)
	}
}

// TestTemplateConfig: the stored requests read back with empty lists, the optional requests as null; a broken one is
// an error.
func TestTemplateConfig(t *testing.T) {
	c := TemplateConfig{Create: RequestTemplate{Method: "POST", URL: "u"}, Update: RequestTemplate{Method: "PUT",
		URL: "v"}}
	want := `{"create":{"method":"POST","url":"u","headers":[],"body":null,"extract":[]},` +
		`"update":{"method":"PUT","url":"v","headers":[],"body":null,"extract":[]},"open_thread":null,` +
		`"reply_in_thread":null}`
	if got := string(c.JSON()); got != want {
		t.Errorf("json %s", got)
	}
	back, err := ParseTemplateConfig([]byte(want))
	if err != nil || back.Create.URL != "u" || back.request(RequestOpenThread) != nil ||
		back.request(RequestUpdate).URL != "v" || back.request("nope") != nil {
		t.Errorf("parse %+v %v", back, err)
	}
	if _, err := ParseTemplateConfig([]byte("[")); err == nil {
		t.Error("parsed broken requests")
	}
	full := chatConfig("https://x")
	full.Create.Extract[0].Name = "id"
	if !slices.Equal(full.readable(RequestCreate), nil) || !slices.Equal(full.readable(RequestOpenThread),
		[]string{"id"}) || !slices.Equal(full.readable(RequestReplyInThread), []string{"id", "thread"}) {
		t.Error("readable values")
	}
}

// TestRefs: the Secrets and values a template reads by name — as .F.name, $.F.name or index with a quoted name, in
// every kind of node — and the first other use of the field, which is refused.
func TestRefs(t *testing.T) {
	src := `{{ .Secrets.a }}{{ if $.Secrets.b }}{{ index .Secrets "c" }}{{ end }}{{ (.Response.d) }}` +
		`{{ define "x" }}{{ template "x" $.Secrets.e }}{{ end }}{{ range $i, $v := .Alerts }}{{ $v.Labels.x }}{{ end }}`
	if names, _, err := refsIn(src, fieldSecrets); err != nil || !slices.Equal(names, []string{"a", "b", "c", "e"}) {
		t.Errorf("secrets %v %v", names, err)
	}
	if names, _, err := refsIn(src, fieldResponse); err != nil || !slices.Equal(names, []string{"d"}) {
		t.Errorf("values %v %v", names, err)
	}
	for _, bare := range []string{"{{ ($).Secrets.nope }}", "{{ (.).Secrets.x }}", "{{ toJson ($).Secrets }}",
		"{{ .Secrets }}", "{{ $.Secrets }}", "{{ (.Secrets).a }}", "{{ $x := 1 }}{{ $x.Secrets }}",
		`{{ index .Secrets "a" "b" }}`, "{{ template \"t\" .Secrets }}{{ define \"t\" }}{{ end }}"} {
		if _, pos, err := refsIn(bare, fieldSecrets); !errors.Is(err, errBareReference) || pos <= 0 {
			t.Errorf("%s: %d %v", bare, pos, err)
		}
	}
	if names, _, err := refsIn("{{", fieldSecrets); names != nil || err != nil {
		t.Errorf("a broken template %v %v", names, err)
	}
	if l, c := position("ab\ncd", 4); l != 2 || c != 2 {
		t.Errorf("position %d:%d", l, c)
	}
}

// TestMention is `mention` in a request template (C-15.FR-11): everyone as @ and the choice of the settings, a group
// by its name, a User as @ and the login; anything but a target of .Mentions is an error.
func TestMention(t *testing.T) {
	targets := mentionsOf([]mentions.Target{{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneHere},
		{Kind: mentions.TargetEveryone}, {Kind: mentions.TargetGroup, Group: "oncall"},
		{Kind: mentions.TargetUser, User: &mentions.User{PublicID: "SRA", Name: "Alice", Login: "alice"}},
		{Kind: mentions.TargetUser}, {Kind: "other"}})
	var got []string
	for _, m := range targets {
		s, err := mentionText(m)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if !slices.Equal(got, []string{"@here", "@all", "oncall", "@alice"}) || targets[3].ID != "SRA" ||
		targets[3].Name != "Alice" {
		t.Errorf("mentions %v %+v", got, targets)
	}
	if _, err := mentionText("all"); err == nil {
		t.Error("a string was a target")
	}
	if _, err := mentionText(Mention{Type: "x"}); err == nil {
		t.Error("an unknown target rendered")
	}
}

// TestRenderer: a request renders with the data, the URL trimmed and absolute, the headers in order; a Secret the
// Destination does not have, a value no response gave, a URL that is not http, a header with a line break and a
// failing template are errors naming the field; with fill, every name a template reads is filled.
func TestRenderer(t *testing.T) {
	r := renderer{sandbox: sandbox()}
	c := chatConfig("https://chat.example.org")
	d := dataOf(delivery.RequestState{Group: &templates.Data{Status: "firing",
		AlertGroup: templates.AlertGroup{Number: 7, Title: "Disk", Status: "firing"}}})
	d.Secrets, d.Response = map[string]string{"token": "s"}, map[string]string{"id": "m1", "thread": "t1"}
	d.Event, d.Notify = "alerts_added", true
	b, err := r.request(RequestReplyInThread, *c.ReplyInThread, d)
	if err != nil || b.method != "POST" || b.url != "https://chat.example.org/chat/ops/threads/t1/messages" ||
		b.header[0] != [2]string{"X-Key", "s"} || string(b.body) != `{"text":"alerts_added true"}` {
		t.Errorf("reply %+v %v", b, err)
	}
	for name, rt := range map[string]RequestTemplate{
		"secret":     {URL: "https://x/{{ .Secrets.nope }}"},
		"value":      {URL: "https://x/{{ .Response.nope }}"},
		"bare":       {URL: "https://x/{{ .Secrets }}"},
		"not http":   {URL: "ftp://x"},
		"parse":      {URL: "https://x/{{"},
		"run":        {URL: "https://x/{{ len 3 }}"},
		"header":     {URL: "https://x", Headers: []Header{{Name: "A", Value: "{{ .Secrets.nope }}"}}},
		"line break": {URL: "https://x", Headers: []Header{{Name: "A", Value: `a{{ printf "%c" 10 }}b`}}},
		"body":       {URL: "https://x", Body: str("{{ .Response.nope }}")},
		"no group":   {URL: "https://x", Body: str("{{ .Storm.Route }}")},
	} {
		if _, err := r.request("update", rt, d); err == nil || !strings.HasPrefix(err.Error(), "update/") {
			t.Errorf("%s: %v", name, err)
		}
	}
	filled := renderer{sandbox: sandbox(), fill: masked}
	if out, err := filled.field("x", "{{ .Secrets.a }} {{ .Response.b }}", RequestData{}); err != nil ||
		out != "[redacted] example-b" {
		t.Errorf("filled %q %v", out, err)
	}
	// The whole data, printed or encoded, shows no Secret.
	d.Secrets = SecretValues{"token": "s3cr3t"}
	for _, src := range []string{"{{ toJson . }}", `{{ printf "%v" . }}`, "{{ . }}", "{{ toJson .Secrets }}x"} {
		if out, err := r.field("x", "{{ $s := 1 }}"+src, d); strings.Contains(out, "s3cr3t") {
			t.Errorf("%s leaked: %q %v", src, out, err)
		}
	}
	if ex := exampleData(t0, RequestCreate); ex.Event != "" || !ex.Notify || len(ex.Mentions) != 3 {
		t.Errorf("example %+v", ex)
	}
	st := dataOf(delivery.RequestState{Storm: &delivery.StormState{Route: "db", AlertGroupCount: 30}})
	if st.AlertGroup != nil || st.Storm.Route != "db" || st.Alerts == nil {
		t.Errorf("storm data %+v", st)
	}
}

// TestTemplateWarnings: a literal credential in the URL or the Authorization header of any request warns at its field.
func TestTemplateWarnings(t *testing.T) {
	c := chatConfig("https://x")
	c.Update.URL = "https://x/?key=abc"
	c.ReplyInThread.Headers = []Header{{Name: "Authorization", Value: "Bearer abc"}}
	got := TemplateWarnings("/template", c)
	want := []Warning{{Kind: WarningLiteralCredential, Field: "/template/update/url"},
		{Kind: WarningLiteralCredential, Field: "/template/reply_in_thread/headers/0/value"}}
	if !slices.Equal(got, want) {
		t.Errorf("warnings %+v", got)
	}
	// A Secret read through $. or index is a reference, not a literal (D291).
	for _, url := range []string{"https://x/?k={{ $.Secrets.k }}", `https://x/?k={{ index .Secrets "k" }}`} {
		if w := Warnings("/events", EventsConfig{URL: url}); len(w) != 0 {
			t.Errorf("%s: %+v", url, w)
		}
	}
}
