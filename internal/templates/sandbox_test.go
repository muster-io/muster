// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package templates

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
)

var t0 = time.Date(2026, 10, 4, 14, 5, 0, 0, time.UTC)

func sandbox() *Sandbox {
	return New(clock.NewManual(t0), clock.NewManual(t0))
}

func render(t *testing.T, src string, data any) (string, error) {
	t.Helper()
	tmpl, err := sandbox().Parse("t", src)
	if err != nil {
		return "", err
	}
	return tmpl.Execute(data)
}

func templateError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want a template error", err)
	}
	return e
}

// TestSandboxRefusesUnregistered: env, readFile, an unknown name and the functions sprout keeps out of the chosen
// registries do not exist; the error names the function with its line and column.
func TestSandboxRefusesUnregistered(t *testing.T) {
	for _, tc := range []struct {
		src          string
		name         string
		line, column int
	}{
		{`{{ env "HOME" }}`, "env", 1, 4},
		{"x\n  {{ readFile \"/etc/passwd\" }}", "readFile", 2, 6},
		{`{{ if true }}{{ .X | nosuchthing }}{{ end }}`, "nosuchthing", 1, 22},
		{`{{ expandenv "$HOME" }}`, "expandenv", 1, 4},
		{`{{ getHostByName "example.org" }}`, "getHostByName", 1, 4},
		{`{{ uuidv4 }}`, "uuidv4", 1, 4},
		{`{{ define "x" }}{{ osBase "/" }}{{ end }}ok`, "osBase", 1, 20},
		{`{{ shuffle "abc" }}`, "shuffle", 1, 4},
		{`{{ $d := dict }}{{ $_ := set "a" $d $d }}`, "set", 1, 26},
		{`{{ merge (dict) (dict) }}`, "merge", 1, 4},
	} {
		_, err := sandbox().Parse("t", tc.src)
		e := templateError(t, err)
		if e.Code != CodeUnknownFunction || e.Line != tc.line || e.Column != tc.column ||
			!strings.Contains(e.Detail, `"`+tc.name+`"`) {
			t.Errorf("%s: %+v, want unknown_function %s at %d:%d", tc.src, e, tc.name, tc.line, tc.column)
		}
	}
	if e := templateError(t, func() error { _, err := sandbox().Parse("t", "a\n{{ if }}"); return err }()); e.Code !=
		CodeSyntax || e.Line != 2 {
		t.Errorf("syntax error = %+v, want template_syntax on line 2", e)
	}
	if !strings.Contains((&Error{Line: 2, Column: 3, Detail: "x"}).Error(), "line 2, column 3") ||
		(&Error{Line: 2, Detail: "x"}).Error() != "line 2: x" || (&Error{Detail: "x"}).Error() != "x" {
		t.Error("Error texts")
	}
	if names := sandbox().Names(); len(names) < 100 || !contains(names, "now") || !contains(names, "reReplaceAll") ||
		contains(names, "env") {
		t.Errorf("names = %d", len(names))
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// TestSandboxCaps: until, untilStep, seq, repeat, indent and a range over an integer stop at LoopCap; output stops at
// OutputCap; values a template builds but never writes are bounded; ranges, template calls and recursion use up the
// budget of the execution; printf refuses huge widths.
func TestSandboxCaps(t *testing.T) {
	for _, src := range []string{
		`{{ range until 1001 }}{{ end }}`,
		`{{ range untilStep 0 5000 1 }}{{ end }}`,
		`{{ seq 1001 }}`,
		`{{ seq 1 1 2000 }}`,
		`{{ repeat 1001 "x" }}`,
		`{{ indent 5000 "x" }}`,
		`{{ nindent 5000 "x" }}`,
		`{{ range 1001 }}{{ end }}`,
		`{{ range $i := 2000 }}{{ end }}`,
		// 50 doublings of a string that is never written.
		`{{ $s := "xx" }}{{ range until 50 }}{{ $s = printf "%s%s" $s $s }}{{ end }}`,
		`{{ $s := "xx" }}{{ range until 50 }}{{ $s = print $s $s }}{{ end }}`,
		`{{ $l := list 1 2 }}{{ range until 50 }}{{ $l = concat $l $l }}{{ end }}`,
		// Nested ranges visit far more than the budget.
		`{{ range until 1000 }}{{ range until 1000 }}{{ end }}{{ end }}`,
		`{{ define "a" }}{{ template "a" . }}{{ end }}{{ template "a" . }}`,
		`{{ define "a" }}{{ template "a" }}{{ end }}{{ template "a" }}`,
		`{{ printf "%0100000000d" 1 }}`,
		`{{ printf "%.*d" 100000000 1 }}`,
		`{{ range until 1000 }}{{ repeat 1000 "x" }}{{ end }}`,
		// Single calls that would build far more than they may.
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ $s = repeat 1000 $s }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ replace "" $s $s }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ join $s (until 1000) }}`,
		`{{ $s := repeat 1000 "x\n" }}{{ indent 1000 $s }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ wrapWith 1 $s $s }}`,
		`{{ wrap 0 "x" }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ regexReplaceAll "." $s $s }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ reReplaceAll "(.*)" (repeat 30 "$1") $s }}`,
		`{{ $s := repeat 1000 "xxxxxxxxxx" }}{{ regexReplaceAllLiteral "x" $s $s }}`,
		`{{ regexReplaceAll "(" "" "" }}`,
		`{{ chunk 0 (list 1 2) }}`,
		// A list of many references to one long string is counted at every place, before fmt prints it.
		`{{ $s := repeat 500 "xxxxxxxxxx" }}{{ $l := list }}{{ range until 100 }}{{ range until 100 }}` +
			`{{ $l = append $l $s }}{{ end }}{{ end }}`,
		`{{ $l := list (dict "a" (list 1 2)) }}{{ range until 30 }}{{ $l = concat $l $l }}{{ end }}`,
	} {
		if _, err := render(t, src, nil); err == nil {
			t.Errorf("%s: no error", src)
		} else if e := templateError(t, err); e.Code != CodeSyntax {
			t.Errorf("%s: %+v", src, e)
		}
	}
	out, err := render(t, `{{ range until 1000 }}{{ end }}{{ len (until 1000) }} {{ seq 3 }} {{ seq 3 1 }} {{ seq 1 2 5 }}`+
		` {{ seq 1 0 5 }}{{ seq }}{{ repeat 3 "ab" }} {{ repeat -1 "x" }}{{ until -2 }} {{ untilStep 5 0 -2 }}`+
		` {{ untilStep 0 5 0 }}{{ indent 2 "a\nb" }}|{{ nindent 1 "c" }}|{{ printf "%5.2f" 3.14159 }}`+
		`{{ range 3 }}{{ . }}{{ end }}`, nil)
	want := "1000 1 2 3 3 2 1 1 3 5 ababab [0 -1] [5 3 1] []  a\n  b|\n c| 3.14012"
	if err != nil || out != want {
		t.Errorf("bounded functions = %q, %v; want %q", out, err, want)
	}
	out, err = render(t, `{{ replace "a" "b" "aa" }} {{ join "," (list 1 2) }} {{ wrap 3 "ab cd" }} `+
		`{{ wrapWith 2 "|" "ab cd" }} {{ regexReplaceAll "a(.)" "$1" "abac" }} {{ regexReplaceAllLiteral "a" "$1" "ab" }} `+
		`{{ reReplaceAll "b" "c" "ab" }} {{ indent 1 "x" }}`, nil)
	if want := "bb 1,2 ab\ncd ab|cd bc $1b ac  x"; err != nil || out != want {
		t.Errorf("guarded functions = %q, %v; want %q", out, err, want)
	}
	// sprout's regex registry, not the deprecated regexp one: the string to work on is the last argument.
	out, err = render(t, `{{ regexFindAll "a." 2 "abacad" }} {{ "a,b,c" | regexSplit "," -1 }} `+
		`{{ "x1" | regexMatch "[0-9]" }} {{ "abac" | regexReplaceAll "a" "o" }}`, nil)
	if want := "[ab ac] [a b c] true oboc"; err != nil || out != want {
		t.Errorf("regular expressions = %q, %v; want %q", out, err, want)
	}
	if _, err := render(t, `{{ range $k, $v := . }}{{ $k }}{{ end }}`,
		map[string]int{"a": 1}); err != nil {
		t.Errorf("range over a map: %v", err)
	}
	list := make([]string, 100)
	for i := range list {
		list[i] = strings.Repeat("z", 5000)
	}
	for _, src := range []string{`{{ print . }}`, `{{ println . }}`, `{{ printf "%v" . }}`, `{{ join "," . }}`} {
		if _, err := render(t, src, list); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("%s of 500,000 bytes: %v", src, err)
		}
	}
	if out, err := render(t, `{{ print . }}`, struct {
		A [2]string
		M map[string][]int
		p int
	}{A: [2]string{"x", "y"}, M: map[string][]int{"k": {1}}}); err != nil || out != "{[x y] map[k:[1]] 0}" {
		t.Errorf("print of a struct = %q, %v", out, err)
	}
	if _, err := render(t, `{{ range . }}{{ end }}`, make(chan int)); err == nil {
		t.Error("range over a channel: no error")
	}
	big := strings.Repeat("y", OutputCap)
	if out, err := render(t, `{{ . }}`, big); err != nil || len(out) != OutputCap {
		t.Errorf("output at the cap: %d, %v", len(out), err)
	}
	if _, err := render(t, `{{ . }}z`, big); err == nil {
		t.Error("output past the cap: no error")
	}
	if _, err := render(t, `{{ range until 1000 }}{{ range until 51 }}é{{ end }}{{ end }}`, nil); err == nil ||
		!strings.Contains(err.Error(), "longer than") {
		t.Errorf("a loop writing past the cap: %v", err)
	}
	if out, err := render(t, `{{ define "a" }}{{ if . }}{{ template "a" (sub . 1) }}{{ end }}{{ . }}{{ end }}`+
		`{{ template "a" 3 }}`, nil); err == nil || out != "" {
		t.Errorf("sub is not registered: %q %v", out, err)
	}
}

// TestSandboxExecutionLimit: a guard stops an execution that ran past ExecutionLimit on the real clock.
func TestSandboxExecutionLimit(t *testing.T) {
	stepping := &steppingClock{now: t0, step: time.Second}
	s := New(clock.NewManual(t0), stepping)
	tmpl, err := s.Parse("t", `{{ range until 10 }}{{ range until 10 }}{{ end }}{{ end }}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Execute(nil); err == nil || !strings.Contains(err.Error(), "ran for longer") {
		t.Errorf("err = %v", err)
	}
	tmpl, err = s.Parse("t", `{{ define "a" }}x{{ end }}{{ template "a" }}{{ template "a" }}{{ template "a" }}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Execute(nil); err == nil {
		t.Error("template calls past the limit: no error")
	}
}

// steppingClock moves by step at every reading.
type steppingClock struct {
	now  time.Time
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	c.now = c.now.Add(c.step)
	return c.now
}

// TestSandboxAlertmanagerTemplates: templates written for Alertmanager run unchanged, with its function names and
// the methods of its data; now and since follow Muster's business clock.
func TestSandboxAlertmanagerTemplates(t *testing.T) {
	data := Data{Status: StatusFiring, ExternalURL: "http://am:9093", Receiver: "muster",
		GroupLabels:       KV{"alertname": "DiskFull"},
		CommonLabels:      KV{"alertname": "DiskFull", "cluster": "prod"},
		CommonAnnotations: KV{"summary": "disk is full"},
		Alerts: Alerts{
			{Status: StatusFiring, Labels: KV{"alertname": "DiskFull", "instance": "db-1", "cluster": "prod"},
				Annotations: KV{"description": "95% used"}, StartsAt: t0.Add(-90 * time.Minute)},
			{Status: StatusResolved, Labels: KV{"alertname": "DiskFull", "instance": "db-2", "cluster": "prod"},
				StartsAt: t0.Add(-2 * time.Hour), EndsAt: t0},
		},
		AlertGroup: AlertGroup{Number: 7, Title: "DiskFull", URL: "http://muster/alert-groups/AG1"},
	}
	for _, tc := range []struct{ src, want string }{
		{`{{ define "__subject" }}[{{ .Status | toUpper }}{{ if eq .Status "firing" }}:{{ .Alerts.Firing | len }}` +
			`{{ end }}] {{ .GroupLabels.SortedPairs.Values | join " " }}{{ end }}{{ template "__subject" . }}`,
			"[FIRING:1] DiskFull"},
		{`{{ range .Alerts.Firing }}{{ .Labels.instance }}: {{ .Annotations.description }}{{ end }}` +
			`{{ range .Alerts.Resolved }} ok {{ .Labels.instance }}{{ end }}`, "db-1: 95% used ok db-2"},
		{`{{ range .CommonLabels.SortedPairs }}{{ .Name }}={{ .Value }} {{ end }}`, "alertname=DiskFull cluster=prod "},
		{`{{ .CommonLabels.Remove (stringSlice "alertname") | len }} {{ .CommonLabels.Names | join "," }}`,
			"1 alertname,cluster"},
		{`{{ reReplaceAll "db-(\\d)" "node $1" "db-1" }} {{ match "^Disk" .GroupLabels.alertname }}`, "node 1 true"},
		{`{{ title "disk full now" }} {{ toLower "ABC" }} {{ trimSpace "  x " }} {{ safeHtml "<b>" }}`,
			"Disk Full Now abc x <b>"},
		{`{{ (index .Alerts 0).StartsAt | date "15:04" }} {{ ((index .Alerts 0).StartsAt | tz "Europe/Moscow").Hour }}`,
			"12:35 15"},
		{`{{ humanizeDuration 93784 }} {{ humanizeDuration 0.0125 }} {{ humanizeDuration "61" }} ` +
			`{{ humanizeDuration 0 }} {{ humanizeDuration -3600 }} {{ humanizeDuration 1.5 }}`,
			"1d 2h 3m 4s 12.5ms 1m 1s 0s -1h 0m 0s 1.5s"},
		{`{{ since (index .Alerts 0).StartsAt }} {{ now.Format "15:04" }}`, "1h30m0s 14:05"},
		{`{{ urlUnescape "a%20b" }} {{ safeUrl "http://x" }} {{ toJson .GroupLabels }}`,
			`a b http://x {"alertname":"DiskFull"}`},
		{`{{ .AlertGroup.Number }} {{ .ExternalURL }} {{ .Receiver }} {{ regexReplaceAll "a" "o" "banana" }}`,
			"7 http://am:9093 muster bonono"},
		{`{{ .CommonLabels.Values | join "/" }} {{ (.CommonLabels.SortedPairs).Names | len }}`, "DiskFull/prod 2"},
	} {
		out, err := render(t, tc.src, data)
		if err != nil || out != tc.want {
			t.Errorf("%s = %q, %v; want %q", tc.src, out, err, tc.want)
		}
	}
	line := LineData{Alert: data.Alerts[0], AlertGroup: data.AlertGroup}
	if out, err := render(t, `{{ .Labels.instance }} of #{{ .AlertGroup.Number }}{{ .Labels.missing }}`, line); err !=
		nil || out != "db-1 of #7<no value>" {
		t.Errorf("line = %q, %v", out, err)
	}
	for _, src := range []string{`{{ tz "Nowhere/City" now }}`, `{{ match "(" "x" }}`, `{{ reReplaceAll "(" "" "" }}`,
		`{{ humanizeDuration "x" }}`, `{{ humanizeDuration true }}`, `{{ index .Alerts 9 }}`} {
		if _, err := render(t, src, data); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
	for _, v := range []any{float32(2), int64(2), int32(2), uint(2), uint64(2), 2 * time.Second} {
		if s, err := humanizeDuration(v); err != nil || s != "2s" {
			t.Errorf("humanizeDuration(%T) = %q, %v", v, s, err)
		}
	}
}

// TestFromWebhook reads the template data of a Stored Snapshot as Alertmanager sent it.
func TestFromWebhook(t *testing.T) {
	d, err := FromWebhook([]byte(`{"receiver":"muster","status":"firing","groupLabels":{"alertname":"A"},
		"alerts":[{"status":"firing","labels":{"alertname":"A"},"startsAt":"2026-10-04T14:05:00+03:00",
		"fingerprint":"f1","generatorURL":"http://p"}]}`))
	if err != nil || d.Status != "firing" || len(d.Alerts) != 1 || d.Alerts[0].StartsAt.Location() != time.UTC ||
		d.Alerts[0].Fingerprint != "f1" || d.GroupLabels["alertname"] != "A" || d.CommonLabels == nil ||
		d.Alerts[0].Annotations == nil {
		t.Errorf("data = %+v, %v", d, err)
	}
	for _, body := range []string{`nope`, `{}`} {
		if _, err := FromWebhook([]byte(body)); !errors.Is(err, ErrNotWebhook) {
			t.Errorf("%s: %v", body, err)
		}
	}
	var kv KV
	if kv.Remove([]string{"a"}) == nil {
		t.Error("Remove of nil")
	}
}

// TestLookupAndLookupTables: lookup reads through the Env of an execution, reads nothing without one, and its errors
// fail the template; LookupTables names the tables read by a literal name, in every template of the source.
func TestLookupAndLookupTables(t *testing.T) {
	src := `{{ define "x" }}{{ lookup "dc" "a" "b" }}{{ end }}{{ lookup "grafana" .K "address" }}|{{ lookup $.T "k" "c" }}` +
		`|{{ template "x" }}|{{ lookup "grafana" "k" "uid" }}`
	tmpl, err := sandbox().Parse("t", src)
	if err != nil {
		t.Fatal(err)
	}
	if got := tmpl.LookupTables(); !slices.Equal(got, []string{"dc", "grafana"}) {
		t.Errorf("LookupTables = %v", got)
	}
	data := map[string]string{"K": "prod", "T": "other"}
	out, err := tmpl.Execute(data)
	if err != nil || out != "|||" {
		t.Errorf("without an env: %q %v", out, err)
	}
	var calls []string
	env := Env{Lookup: func(table, key, column string) (string, error) {
		calls = append(calls, table+"/"+key+"/"+column)
		return strings.ToUpper(column), nil
	}}
	out, err = tmpl.ExecuteIn(env, data)
	if err != nil || out != "ADDRESS|C|B|UID" {
		t.Errorf("with an env: %q %v", out, err)
	}
	if !slices.Equal(calls, []string{"grafana/prod/address", "other/k/c", "dc/a/b", "grafana/k/uid"}) {
		t.Errorf("calls %v", calls)
	}
	failing := Env{Lookup: func(string, string, string) (string, error) { return "", errors.New("no database") }}
	if _, err := tmpl.ExecuteIn(failing, data); err == nil || !strings.Contains(err.Error(), "no database") {
		t.Errorf("a failing lookup: %v", err)
	}
}

// TestMentionTokens: mention writes a token for all, channel, here, owner and a group, refuses anything else, and the
// token survives until ReplaceTokens renders it; StripTokens and ReplaceTokens drop stray token characters.
func TestMentionTokens(t *testing.T) {
	out, err := render(t, `{{ mention "all" }} {{ mention "channel" }} {{ mention "here" }} {{ mention "owner" }} `+
		`{{ mention "group" "on-call.db" }} @all`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := Tokens(out); !slices.Equal(got, []Token{{Name: "all"}, {Name: "channel"}, {Name: "here"},
		{Name: "owner"}, {Name: "group", Group: "on-call.db"}}) {
		t.Errorf("tokens %v", got)
	}
	shown := ReplaceTokens(out, func(tk Token) string { return "<" + tk.Name + tk.Group + ">" })
	if shown != "<all> <channel> <here> <owner> <groupon-call.db> @all" {
		t.Errorf("replaced %q", shown)
	}
	for _, src := range []string{`{{ mention "everyone" }}`, `{{ mention "all" "x" }}`, `{{ mention "group" }}`,
		`{{ mention "group" "a b" }}`, `{{ mention "group" "a" "b" }}`} {
		if _, err := render(t, src, nil); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
	stray := "a" + string(tokenStart) + "b" + string(tokenEnd) + string(tokenSep) + "c" + string(tokenStart) + "d"
	if got := StripTokens(stray); got != "abcd" {
		t.Errorf("StripTokens %q", got)
	}
	if got := ReplaceTokens(stray, func(Token) string { return "@" }); got != "a@cd" {
		t.Errorf("ReplaceTokens %q", got)
	}
	nested := string(tokenStart) + "x" + MentionToken(Token{Name: "all"})
	if got := ReplaceTokens(nested, func(tk Token) string { return "@" + tk.Name }); got != "xall" {
		t.Errorf("nested %q", got)
	}
	if got := ReplaceTokens("plain", func(Token) string { return "@" }); got != "plain" {
		t.Errorf("plain %q", got)
	}
	if !slices.Contains(sandbox().Names(), "mention") || !slices.Contains(sandbox().Names(), "lookup") {
		t.Error("mention and lookup are registered")
	}
}

// TestSafeURL: only absolute http and https links without spaces, control or token characters are kept.
func TestSafeURL(t *testing.T) {
	for in, want := range map[string]string{
		" https://grafana.example.org/d/x?a=b ": "https://grafana.example.org/d/x?a=b",
		"HTTP://h.example.org":                  "HTTP://h.example.org",
		"javascript:alert(1)":                   "",
		"data:text/html,x":                      "",
		"//h.example.org/x":                     "",
		"/relative":                             "",
		"https://h.example.org/a b":             "",
		"https://h.example.org/\x00":            "",
		"https://h.example.org/" + MentionToken(Token{Name: "all"}): "",
		"https:opaque":                         "",
		"https://google.com@evil.example.org/": "",
		"https://h.example.org/\u202egpj":      "",
		"https://h.example.org/\u200b":         "",
		"https://[::1":                         "",
		"":                                     "",
		"ftp://h.example.org":                  "",
	} {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
