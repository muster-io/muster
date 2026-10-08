// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/templates"
)

// TestSafeOutput is C-12.FR-7 and C-12.AC-1: `@channel` in alert data gets a zero-width space after `@`, a value
// longer than message.value_cap is cut at a character boundary with "…", only http(s) links are kept, and alert data
// reach templates already escaped for the target markup.
func TestSafeOutput(t *testing.T) {
	if got := Value("@channel and @here"); got != "@\u200bchannel and @\u200bhere" {
		t.Errorf("neutralized %q", got)
	}
	long := strings.Repeat("я", ValueCap) // two bytes each
	cut := Cap(long)
	if len(cut) > ValueCap || !strings.HasSuffix(cut, "…") || !utf8.ValidString(cut) {
		t.Errorf("cut to %d bytes, valid %v", len(cut), utf8.ValidString(cut))
	}
	if Cap("short") != "short" || Cap(strings.Repeat("x", ValueCap)) != strings.Repeat("x", ValueCap) {
		t.Error("a value within the cap changed")
	}
	for in, want := range map[string]string{
		"https://grafana.example.org/d/1": "https://grafana.example.org/d/1",
		"HTTP://prometheus:9090/graph":    "HTTP://prometheus:9090/graph",
		"javascript:alert(1)":             "",
		"data:text/html,x":                "",
		"/relative/path":                  "",
		"ftp://files.example.org/x":       "",
		"http://%zz":                      "",
	} {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q) = %q", in, got)
		}
	}
	if EscapeMarkdown("*a_b* [x](y) `c` ~d~ <e> |f| \\") != `\*a\_b\* \[x\](y) \`+"`"+`c\`+"`"+` \~d\~ \<e\> \|f\| \\` {
		t.Errorf("markdown %q", EscapeMarkdown("*a_b* [x](y) `c` ~d~ <e> |f| \\"))
	}
	if EscapeHTML(`<b>"x" & y</b>`) != "&lt;b&gt;&#34;x&#34; &amp; y&lt;/b&gt;" || Escaper(MarkupPlain)("<b>") != "<b>" {
		t.Error("html and plain escapers")
	}
	d := templates.Data{
		GroupLabels:       templates.KV{"alertname": "A*"},
		CommonLabels:      templates.KV{"owner": "@channel"},
		CommonAnnotations: templates.KV{"note": "<script>"},
		Alerts: templates.Alerts{{Labels: templates.KV{"pod": "p_1"}, Annotations: templates.KV{"x": long},
			GeneratorURL: "javascript:alert(1)"}},
		AlertGroup: templates.AlertGroup{Title: "a_b", Summary: "@all", Route: "r", Owner: "o"},
	}
	md := SafeData(d, MarkupMarkdown)
	if md.CommonLabels["owner"] != "@\u200bchannel" || md.GroupLabels["alertname"] != `A\*` ||
		md.Alerts[0].Labels["pod"] != `p\_1` || md.Alerts[0].GeneratorURL != "" ||
		!strings.HasSuffix(md.Alerts[0].Annotations["x"], "…") || md.AlertGroup.Title != `a\_b` ||
		md.AlertGroup.Summary != "@\u200ball" {
		t.Errorf("markdown data %+v", md)
	}
	html := SafeData(d, MarkupHTML)
	if html.CommonAnnotations["note"] != "&lt;script&gt;" || d.CommonAnnotations["note"] != "<script>" {
		t.Errorf("html data %+v; the input changed: %v", html.CommonAnnotations, d.CommonAnnotations)
	}
	line := SafeLine(d.Alerts[0], d.AlertGroup, MarkupHTML)
	if line.Labels["pod"] != "p_1" || line.AlertGroup.Summary != "@\u200ball" {
		t.Errorf("line data %+v", line)
	}
	// safeHtml returns its already escaped argument unchanged.
	r := newRenderer(t)
	out, err := r.execute("t", `{{ .CommonAnnotations.note | safeHtml }}`, html)
	if err != nil || out != "&lt;script&gt;" {
		t.Errorf("safeHtml = %q, %v", out, err)
	}
}
