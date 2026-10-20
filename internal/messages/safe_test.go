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

// TestEscapeCommonMark is C-22.FR-4 and C-22.AC-5: alert data reach a bridge as literal CommonMark text — every
// punctuation that can start markup inside a line is escaped, and so is the marker of a heading, a list item or a
// setext underline at the start of a line, after any spaces and tabs — a continuation line laid out inside a list item
// is indented relative to it; `@` is neutralized by Value before, so a hostile label value is no Mention, no emphasis,
// no HTML and no link. Blank lines, the last line ending and the spaces and tabs a value starts with are dropped, so
// that no indented code block starts in alert data and a value stays inside its list item or block quote.
func TestEscapeCommonMark(t *testing.T) {
	hostile := "@all *x* <b>y</b> [z](http://evil)"
	if got := EscapeCommonMark(Value(hostile)); got != "@\u200ball \\*x\\* \\<b\\>y\\</b\\> \\[z\\](http://evil)" {
		t.Errorf("hostile value = %q", got)
	}
	for in, want := range map[string]string{
		"a_b `c` ~~d~~ &amp; \\":          "a\\_b \\`c\\` \\~\\~d\\~\\~ \\&amp; \\\\",
		"![i](http://x/i.png) <http://x>": "!\\[i\\](http://x/i.png) \\<http://x\\>",
		"# heading":                       "\\# heading",
		"   ## three spaces":              "\\## three spaces",
		"    # four spaces":               "\\# four spaces",
		"\t# tab":                         "\\# tab",
		"    code":                        "code",
		"a\n    # head":                   "a\n    \\# head",
		"a\n\t# head":                     "a\n\t\\# head",
		"a\n\t- x":                        "a\n\t\\- x",
		"a\n    - x":                      "a\n    \\- x",
		"a\n \t + x":                      "a\n \t \\+ x",
		"a\n    1. x":                     "a\n    1\\. x",
		"a\n\t1) x":                       "a\n\t1\\) x",
		"a\n    ===":                      "a\n    \\===",
		"a\n\t\t---":                      "a\n\t\t\\---",
		"a\n    * * *":                    "a\n    \\* \\* \\*",
		"a\n\t___":                        "a\n\t\\_\\_\\_",
		"a\n    > quote":                  "a\n    \\> quote",
		"a\n\t```go":                      "a\n\t\\`\\`\\`go",
		"a\n    ~~~":                      "a\n    \\~\\~\\~",
		"- item":                          "\\- item",
		"+ item":                          "\\+ item",
		"* item":                          "\\* item",
		"> quote":                         "\\> quote",
		"===":                             "\\===",
		"a\n---\nb":                       "a\n\\---\nb",
		"1. one":                          "1\\. one",
		"12) twelve":                      "12\\) twelve",
		"123456789. nine digits":          "123456789\\. nine digits",
		"1234567890. ten digits":          "1234567890. ten digits",
		"1x. not a number":                "1x. not a number",
		"v1. mid-line # - + = 2.":         "v1. mid-line # - + = 2.",
		"x\r# carriage return":            "x\r\\# carriage return",
		"x\r\n- crlf":                     "x\r\n\\- crlf",
		"\n\n= blank lines":               "\\= blank lines",
		"pod: a\n\n      code *x*":        "pod: a\n      code \\*x\\*",
		"env\n\n    code":                 "env\n    code",
		"\n\n\tcode":                      "code",
		"web\n \t\n\nrest *x*":            "web\nrest \\*x\\*",
		"a\r\n \t\r\n\r\n    code":        "a\r\n    code",
		"a\r\r \r    code":                "a\r    code",
		"a\n\n":                           "a",
		" \t\n  a\n\t \n":                 "a",
		"   ":                             "",
		"":                                "",
	} {
		if got := EscapeCommonMark(in); got != want {
			t.Errorf("EscapeCommonMark(%q) = %q, want %q", in, got, want)
		}
	}
	if Escaper(MarkupCommonMark)("*a*") != "\\*a\\*" {
		t.Error("the escaper of commonmark")
	}
	d := templates.Data{CommonLabels: templates.KV{"note": hostile},
		Alerts: templates.Alerts{{Labels: templates.KV{"pod": "1. p_1"}}}}
	cm := SafeData(d, MarkupCommonMark)
	if cm.CommonLabels["note"] != "@\u200ball \\*x\\* \\<b\\>y\\</b\\> \\[z\\](http://evil)" ||
		cm.Alerts[0].Labels["pod"] != "1\\. p\\_1" {
		t.Errorf("commonmark data %+v", cm)
	}
	if line := SafeLine(d.Alerts[0], d.AlertGroup, MarkupCommonMark); line.Labels["pod"] != "1\\. p\\_1" {
		t.Errorf("commonmark line data %+v", line)
	}
}

// ac6 is the label value of C-24.AC-6, and ac6Mrkdwn the text it reaches Slack as: entities for `&`, `<` and `>`, and
// a zero-width space after `@` and each emphasis character.
const (
	ac6       = "@channel <!here> <b>x</b> *y* <https://evil.example|z> &"
	ac6Mrkdwn = "@\u200bchannel &lt;!here&gt; &lt;b&gt;x&lt;/b&gt; *\u200by*\u200b &lt;https://evil.example|z&gt; &amp;"
)

// TestEscapeMrkdwn is C-24.FR-7 and C-24.AC-6: alert data reach Slack as literal mrkdwn text — `&`, `<` and `>` as
// entities, so that nothing becomes a link, a Mention, <!channel> or <!here>, and a zero-width space after `@`, `*`,
// `_`, `~` and the backquote, so that nothing pairs into emphasis, strikethrough or code; an `@` that Value
// neutralized gets no second one, and an entity in the input is escaped again, since the escaper runs once.
func TestEscapeMrkdwn(t *testing.T) {
	if got := EscapeMrkdwn(Value(ac6)); got != ac6Mrkdwn {
		t.Errorf("C-24.AC-6 value = %q, want %q", got, ac6Mrkdwn)
	}
	for in, want := range map[string]string{
		"":                       "",
		"plain text · no markup": "plain text · no markup",
		"**bold**":               "*\u200b*\u200bbold*\u200b*\u200b",
		"__italic__":             "_\u200b_\u200bitalic_\u200b_\u200b",
		"~~struck~~":             "~\u200b~\u200bstruck~\u200b~\u200b",
		"``code``":               "`\u200b`\u200bcode`\u200b`\u200b",
		"@here":                  "@\u200bhere",
		"@\u200bhere":            "@\u200bhere",
		"a*\u200bb":              "a*\u200bb",
		"<@U0123ABCD>":           "&lt;@\u200bU0123ABCD&gt;",
		"<!channel>":             "&lt;!channel&gt;",
		"&amp; &lt;":             "&amp;amp; &amp;lt;",
		"|bar|":                  "|bar|",
		"я_я":                    "я_\u200bя",
		"end*":                   "end*\u200b",
	} {
		if got := EscapeMrkdwn(in); got != want {
			t.Errorf("EscapeMrkdwn(%q) = %q, want %q", in, got, want)
		}
	}
	if Escaper(MarkupMrkdwn)("*a*") != "*\u200ba*\u200b" {
		t.Error("the escaper of mrkdwn")
	}
}

// TestNeutralizeMrkdwnMentions is C-12.FR-8 and C-24.FR-6 for template text in mrkdwn: a literal <!here>, <!channel>,
// <!everyone>, a user group and <@U…> show as written and notify nobody, links stay links, other text is kept, and
// neutralizing twice changes nothing.
func TestNeutralizeMrkdwnMentions(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"*bold* _x_ ~y~":                   "*bold* _x_ ~y~",
		"<!here> <!channel> <!everyone>":   "&lt;!here> &lt;!channel> &lt;!everyone>",
		"<!subteam^S0123|@sre>":            "&lt;!subteam^S0123|@sre>",
		"<@U0123ABCD> <@\u200bU0123ABCD>":  "&lt;@U0123ABCD> &lt;@\u200bU0123ABCD>",
		"<https://e.org|runbook> <#C0123>": "<https://e.org|runbook> <#C0123>",
		"&lt;!here>":                       "&lt;!here>",
	} {
		got := NeutralizeMrkdwnMentions(in)
		if got != want || NeutralizeMrkdwnMentions(got) != got {
			t.Errorf("NeutralizeMrkdwnMentions(%q) = %q, want %q", in, got, want)
		}
	}
}

// hostileURL is a generator URL that url.Parse accepts with a host and that, written raw into a mrkdwn link, would
// close it and open a second link whose text the alert chooses; hostileURLMrkdwn is how it reaches a template in
// mrkdwn.
const (
	hostileURL       = "https://a.example/x?q=1><https://phish.example|Open_runbook"
	hostileURLMrkdwn = "https://a.example/x?q=1%3E%3Chttps://phish.example%7COpen_runbook"
)

// TestSafeDataMrkdwn is C-12.FR-7 and C-24.FR-7 for templates: in mrkdwn the label and annotation values and the
// title, summary, Route and Owner of the Alert Group reach a template neutralized and escaped for mrkdwn, a label name
// keeps its spelling, a generator URL is kept only when it is http(s), every URL — the generator URLs, ExternalURL
// and the Alert Group's — is escaped for a link, so that a Route template's `<{{ .GeneratorURL }}|Source>` stays one
// link, and the input does not change.
func TestSafeDataMrkdwn(t *testing.T) {
	d := templates.Data{
		GroupLabels:       templates.KV{"alertname": "Disk*Full"},
		CommonLabels:      templates.KV{"note": ac6, "team_name": "web"},
		CommonAnnotations: templates.KV{"summary": "a_b ~c~ `d`"},
		ExternalURL:       "https://muster.example.org/a|b",
		Alerts: templates.Alerts{
			{Labels: templates.KV{"pod": "<p1>"}, Annotations: templates.KV{"x": "&"},
				GeneratorURL: "javascript:alert(1)"},
			{Labels: templates.KV{"pod": "p2"}, GeneratorURL: "https://prometheus.example.org/graph?g0.expr=up&x=1"},
			{Labels: templates.KV{"pod": "p3"}, GeneratorURL: hostileURL},
		},
		AlertGroup: templates.AlertGroup{Title: "*t*", Summary: "@all", Route: "r_1", Owner: "<!channel>",
			URL: "https://muster.example.org/alert-groups/AG<1>"},
	}
	md := SafeData(d, MarkupMrkdwn)
	if md.CommonLabels["note"] != ac6Mrkdwn || md.CommonLabels["team_name"] != "web" ||
		md.GroupLabels["alertname"] != "Disk*\u200bFull" ||
		md.CommonAnnotations["summary"] != "a_\u200bb ~\u200bc~\u200b `\u200bd`\u200b" ||
		md.Alerts[0].Labels["pod"] != "&lt;p1&gt;" || md.Alerts[0].Annotations["x"] != "&amp;" ||
		md.Alerts[0].GeneratorURL != "" ||
		md.Alerts[1].GeneratorURL != "https://prometheus.example.org/graph?g0.expr=up&amp;x=1" ||
		md.Alerts[2].GeneratorURL != hostileURLMrkdwn || md.ExternalURL != "https://muster.example.org/a%7Cb" {
		t.Errorf("mrkdwn data %+v", md)
	}
	if g := md.AlertGroup; g.Title != "*\u200bt*\u200b" || g.Summary != "@\u200ball" || g.Route != "r_\u200b1" ||
		g.Owner != "&lt;!channel&gt;" || g.URL != "https://muster.example.org/alert-groups/AG%3C1%3E" {
		t.Errorf("mrkdwn alert group %+v", g)
	}
	if d.CommonLabels["note"] != ac6 || d.AlertGroup.Owner != "<!channel>" || d.Alerts[2].GeneratorURL != hostileURL ||
		d.ExternalURL != "https://muster.example.org/a|b" {
		t.Errorf("the input changed: %+v", d)
	}
	line := SafeLine(d.Alerts[0], d.AlertGroup, MarkupMrkdwn)
	if line.Labels["pod"] != "&lt;p1&gt;" || line.AlertGroup.Owner != "&lt;!channel&gt;" || line.GeneratorURL != "" ||
		line.AlertGroup.URL != "https://muster.example.org/alert-groups/AG%3C1%3E" {
		t.Errorf("mrkdwn line data %+v", line)
	}
	// A Route template that writes the generator URL into a link makes one link, whatever the URL holds.
	r := newRenderer(t)
	src := "<{{ .GeneratorURL }}|Source>"
	out, err := r.execute("t", src, SafeLine(d.Alerts[2], d.AlertGroup, MarkupMrkdwn))
	if want := "<" + hostileURLMrkdwn + "|Source>"; err != nil || out != want || strings.Count(out, "<") != 1 ||
		strings.Count(out, "|") != 1 {
		t.Errorf("line template = %q, %v; want %q", out, err, want)
	}
	src = "{{ range .Alerts }}{{ if .GeneratorURL }}<{{ .GeneratorURL }}|Source> {{ end }}{{ end }}"
	want := "<https://prometheus.example.org/graph?g0.expr=up&amp;x=1|Source> <" + hostileURLMrkdwn + "|Source> "
	if out, err = r.execute("t", src, md); err != nil || out != want || strings.Contains(out, "<https://phish") {
		t.Errorf("root template = %q, %v; want %q", out, err, want)
	}
}
