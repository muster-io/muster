// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command llmsgen writes the documentation for AI agents into the built site (C-21.FR-8, C-21.AC-4): a Markdown
// version of every page of the navigation of mkdocs.yml, byte for byte its source; the specifications api/openapi.yaml
// and api/bridge.yaml under api/; llms-full.txt, every page in the order of the navigation with its relative links made
// absolute; and llms.txt, rendered from the template docs/llms.txt.tmpl, whose functions take every value from its
// source. Every link names the version given by -version under the site_url of mkdocs.yml. make docs runs it after the
// build. It fails, naming the file, when a page of the navigation does not exist or has no title, when the template
// names a page, an anchor, a phrase, a setting or an enum that its source no longer has, when a link of either file
// outside code is still relative or does not resolve under the site, when llms.txt links out of the site other than to
// a repository under https://github.com/muster-io/, and when llms.txt is larger than 32 KiB or llms-full.txt is 1 MiB
// or larger.
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

const (
	latest           = "latest"
	mkdocsFile       = "mkdocs.yml"
	llmsName         = "llms.txt"
	fullName         = "llms-full.txt"
	maxLLMSBytes     = 32 << 10
	maxFullBytes     = 1 << 20
	bytesPerToken    = 4
	repositoryPrefix = "https://github.com/muster-io/"
)

// specs are the specifications copied under api/ of the site, the only names spec takes.
var specs = []string{"api/openapi.yaml", "api/bridge.yaml"}

var (
	minorRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

	// fenceRe opens fenced code: three or more backticks with no backtick after them on the line, or three or more
	// tildes.
	fenceRe   = regexp.MustCompile("(?s)^[ \t]*(?:(`{3,})[^`]*|(~{3,}).*)$")
	headingRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*))?$`)
	attrRe    = regexp.MustCompile(`[ \t]*\{[^}]*\}$`)
	hashesRe  = regexp.MustCompile(`(?:^|[ \t]+)#+$`)
	ruleRe    = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	// interruptRe starts a block that ends a paragraph: an admonition, a list item, a table or a block quote.
	interruptRe = regexp.MustCompile(`^ {0,3}(?:!!!|\?\?\?|[-*+](?:[ \t]|$)|\d{1,9}[.)](?:[ \t]|$)|\||>)`)
	// otherRe starts a block that is no paragraph: HTML, an image or a reference definition.
	otherRe = regexp.MustCompile(`^ {0,3}(?:<|!\[|\[[^\]^][^\]]*\]:)`)
	// refDefRe is a reference definition at the start of a line, at any indentation and after the markers of block
	// quotes and list items; a footnote is none.
	refDefRe = regexp.MustCompile(refDef)
	// refDefLineRe finds the reference definitions of a text, line by line.
	refDefLineRe = regexp.MustCompile(`(?m)` + refDef)
	// inlineDestRe finds the destination of an inline link or image: after "](", spaces and at most one line ending,
	// a destination in angle brackets or one without white space, with balanced parentheses one level deep.
	inlineDestRe = regexp.MustCompile(`(?:^|[^\\])\]\([ \t]*(?:\r?\n[ \t]*)?(?:<([^<>\n]*)>|((?:[^\s()<>]|\([^\s()<>]*\))*))`)
	listItemRe   = regexp.MustCompile(`^([-*+]|\d{1,9}[.)])([ \t]+|$)`)
	schemeRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	idRe         = regexp.MustCompile(`\sid="([^"]*)"`)
	urlRe        = regexp.MustCompile("https?://[^\\s<>\"'`|\\[\\]]+")
)

const refDef = `^[ \t]*(?:(?:>[ \t]*|(?:[-*+]|\d{1,9}[.)])[ \t]+))*\[[^\]^\n][^\]\n]*\]:[ \t]*(?:<([^>\n]*)>|(\S+))`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("llmsgen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o options
	flags.StringVar(&o.root, "root", ".", "the repository root, which every other path is relative to")
	flags.StringVar(&o.version, "version", latest, "the version whose URLs the links name: latest or <major>.<minor>")
	flags.StringVar(&o.template, "template", "docs/llms.txt.tmpl", "the template of llms.txt")
	flags.StringVar(&o.defaults, "defaults", "design/prd/l1/defaults.md", "the settings that setting reads")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	usage := ""
	switch {
	case flags.NArg() > 0:
		usage = "llmsgen takes no arguments"
	case o.version != latest && !minorRe.MatchString(o.version):
		usage = fmt.Sprintf("llmsgen: -version %q is not latest or <major>.<minor>", o.version)
	}
	if usage != "" {
		fmt.Fprintln(stderr, usage)
		flags.Usage()
		return 2
	}
	summary, problems := generate(o)
	for _, p := range problems {
		fmt.Fprintln(stderr, p)
	}
	if len(problems) > 0 {
		return 1
	}
	fmt.Fprintln(stdout, summary)
	return 0
}

type options struct {
	root, version, template, defaults string
}

// item is an entry of the navigation: a page, or a section with its entries.
type item struct {
	title    string
	page     *page
	children []*item
}

// page is a page of the navigation; its path is relative to the docs directory.
type page struct {
	path, title, description string
	source                   []byte
}

type mkdocs struct {
	SiteURL string    `yaml:"site_url"`
	DocsDir string    `yaml:"docs_dir"`
	SiteDir string    `yaml:"site_dir"`
	Nav     yaml.Node `yaml:"nav"`
}

type specDoc struct {
	Components struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

type generator struct {
	options
	base, docsDir, siteDir string
	nav                    []*item
	pages                  []*page
	byPath                 map[string]*page
	problems               []string
	reported               map[string]bool
	files                  map[string][]byte
	specDocs               map[string]*specDoc
	ids                    map[string]map[string]bool
	settings               map[string]string
	mapCalls               int
}

// generate writes the outputs into the site and returns the line with their sizes, or the problems found, each
// starting with the file it concerns; a failed run leaves no llms.txt and no llms-full.txt.
func generate(o options) (string, []string) {
	g := &generator{
		options:  o,
		byPath:   map[string]*page{},
		reported: map[string]bool{},
		files:    map[string][]byte{},
		specDocs: map[string]*specDoc{},
		ids:      map[string]map[string]bool{},
	}
	var llms, full string
	if g.load() {
		g.copySources()
		full = g.fullText()
	}
	if len(g.problems) == 0 {
		g.write(g.fullPath(), []byte(full))
		llms = g.render(len(full))
	}
	if len(g.problems) == 0 {
		g.write(g.llmsPath(), []byte(llms))
		g.checkLinks(g.llmsPath(), llms, true)
		g.checkLinks(g.fullPath(), full, false)
		if len(llms) > maxLLMSBytes {
			g.problem("%s: %d bytes; it must be at most 32 KiB (%d bytes)", g.llmsPath(), len(llms), maxLLMSBytes)
		}
		if len(full) >= maxFullBytes {
			g.problem("%s: %d bytes; it must be smaller than 1 MiB (%d bytes)", g.fullPath(), len(full), maxFullBytes)
		}
	}
	if len(g.problems) > 0 {
		if g.siteDir != "" {
			g.removeOutputs()
		}
		return "", g.problems
	}
	return fmt.Sprintf("%s: %d bytes; %s: %d bytes, about %d tokens; %d pages",
		g.llmsPath(), len(llms), g.fullPath(), len(full), len(full)/bytesPerToken, len(g.pages)), nil
}

func (g *generator) llmsPath() string { return path.Join(g.siteDir, llmsName) }

func (g *generator) fullPath() string { return path.Join(g.siteDir, fullName) }

// removeOutputs removes llms.txt and llms-full.txt, so that those of an earlier run never outlive a failed one.
func (g *generator) removeOutputs() {
	for _, f := range []string{g.llmsPath(), g.fullPath()} {
		if err := os.Remove(g.abs(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			g.problem("%s: %v", f, short(err))
		}
	}
}

// problem records a problem once, however often it is found: a template that names a missing page eight times.
func (g *generator) problem(format string, args ...any) {
	p := fmt.Sprintf(format, args...)
	if !g.reported[p] {
		g.reported[p] = true
		g.problems = append(g.problems, p)
	}
}

// fail records the failure of a template function, named after the template, and returns its empty result.
func (g *generator) fail(format string, args ...any) string {
	g.problem("%s: %s", g.template, fmt.Sprintf(format, args...))
	return ""
}

func (g *generator) abs(rel string) string {
	return filepath.Join(g.root, filepath.FromSlash(rel))
}

// short drops the path of a file error, which the message names relative to the root already.
func short(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

// load reads mkdocs.yml and every page of its navigation.
func (g *generator) load() bool {
	data, err := os.ReadFile(g.abs(mkdocsFile))
	if err != nil {
		g.problem("%s: %v", mkdocsFile, short(err))
		return false
	}
	var cfg mkdocs
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		g.problem("%s: %v", mkdocsFile, err)
		return false
	}
	if cfg.SiteURL == "" {
		g.problem("%s: no site_url", mkdocsFile)
	}
	g.base = strings.TrimSuffix(cfg.SiteURL, "/") + "/" + g.version + "/"
	docsDir, siteDir := path.Clean(cmp.Or(cfg.DocsDir, "docs")), path.Clean(cmp.Or(cfg.SiteDir, "site"))
	for _, d := range []string{docsDir, siteDir} {
		if !fs.ValidPath(d) || d == "." {
			g.problem("%s: %s is no directory under the repository root", mkdocsFile, d)
		}
	}
	if len(g.problems) > 0 {
		return false
	}
	g.docsDir, g.siteDir = docsDir, siteDir
	g.removeOutputs()
	if cfg.Nav.Kind != yaml.SequenceNode {
		g.problem("%s: no nav", mkdocsFile)
	}
	if len(g.problems) > 0 {
		return false
	}
	g.nav = g.navItems(&cfg.Nav)
	index := path.Join(g.siteDir, "index.html")
	if _, err := os.Stat(g.abs(index)); err != nil {
		g.problem("%s: %v; build the site first", index, short(err))
	}
	return len(g.problems) == 0
}

func (g *generator) navItems(seq *yaml.Node) []*item {
	items := make([]*item, 0, len(seq.Content))
	for _, n := range seq.Content {
		title, value := "", n
		if n.Kind == yaml.MappingNode && len(n.Content) == 2 && n.Content[0].Kind == yaml.ScalarNode {
			title, value = n.Content[0].Value, n.Content[1]
		}
		switch {
		case value.Kind == yaml.ScalarNode:
			if p := g.navPage(value, title); p != nil {
				items = append(items, &item{page: p})
			}
		case value.Kind == yaml.SequenceNode && value != n:
			items = append(items, &item{title: title, children: g.navItems(value)})
		default:
			g.problem("%s: line %d: a nav entry is a page, a titled page or a titled section", mkdocsFile, n.Line)
		}
	}
	return items
}

func (g *generator) navPage(n *yaml.Node, navTitle string) *page {
	p := n.Value
	if !strings.HasSuffix(p, ".md") || !fs.ValidPath(p) || schemeRe.MatchString(p) {
		g.problem("%s: line %d: the nav entry %s is no Markdown page under %s/", mkdocsFile, n.Line, p, g.docsDir)
		return nil
	}
	file := path.Join(g.docsDir, p)
	if _, dup := g.byPath[p]; dup {
		g.problem("%s: the nav names %s twice", mkdocsFile, file)
		return nil
	}
	src, err := os.ReadFile(g.abs(file))
	if errors.Is(err, fs.ErrNotExist) {
		g.problem("%s: the nav names %s, which does not exist", mkdocsFile, file)
		return nil
	}
	if err != nil {
		g.problem("%s: %v", file, short(err))
		return nil
	}
	title, description := titleAndDescription(string(src))
	if t := strings.TrimSpace(navTitle); t != "" {
		title = t
	}
	if title == "" {
		g.problem("%s: no title: neither the nav of %s nor a # heading gives one", file, mkdocsFile)
	}
	pg := &page{path: p, title: title, description: description, source: src}
	g.byPath[p] = pg
	g.pages = append(g.pages, pg)
	return pg
}

// write writes a file of the site, which is public.
func (g *generator) write(rel string, data []byte) {
	file := g.abs(rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil { //nolint:gosec // the built site is public
		g.problem("%s: %v", rel, short(err))
		return
	}
	if err := os.WriteFile(file, data, 0o644); err != nil { //nolint:gosec // the built site is public
		g.problem("%s: %v", rel, short(err))
	}
}

// copySources writes the Markdown version of every page and the specifications into the site.
func (g *generator) copySources() {
	for _, p := range g.pages {
		g.write(path.Join(g.siteDir, p.path), p.source)
	}
	for _, s := range specs {
		data, err := os.ReadFile(g.abs(s))
		if err != nil {
			g.problem("%s: %v", s, short(err))
			continue
		}
		g.write(path.Join(g.siteDir, s), data)
	}
}

// fullText is llms-full.txt: every page in the order of the navigation after the line with its URL.
func (g *generator) fullText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Muster documentation, version %s\n\n", g.version)
	fmt.Fprintf(&b, "The index of this text, with the rules for AI agents, is %s%s.\n", g.base, llmsName)
	for _, p := range g.pages {
		found := len(g.problems)
		text := g.absoluteLinks(p)
		if len(g.problems) == found {
			g.checkAbsolute(path.Join(g.docsDir, p.path), text, "is not made absolute")
		}
		fmt.Fprintf(&b, "\n<!-- Source: %s%s -->\n", g.base, p.path)
		b.WriteString(strings.TrimRight(text, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// checkAbsolute reports every link or image destination and reference definition of a text, outside fenced code and
// code spans, that is still relative. It finds them on its own, at any indentation, so that it also catches what the
// rewriting leaves, such as a definition in what it takes for indented code.
func (g *generator) checkAbsolute(file, text, wrong string) {
	for _, target := range linkTargets(text) {
		if target != "" && !schemeRe.MatchString(target) {
			g.problem("%s: the link %s %s", file, target, wrong)
		}
	}
}

// linkTargets returns the destinations of the inline links and images and of the reference definitions of a text,
// outside fenced code and code spans.
func linkTargets(src string) []string {
	text := prose(src)
	var out []string
	for _, m := range inlineDestRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1]+m[2])
	}
	for _, m := range refDefLineRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1]+m[2])
	}
	return out
}

// absoluteLinks returns the page's text with its relative links made absolute, except in fenced code.
func (g *generator) absoluteLinks(p *page) string {
	src := string(p.source)
	defs := definitionLines(strings.SplitAfter(src, "\n"))
	var b strings.Builder
	splitFences(src,
		func(first int, text string) { b.WriteString(g.rewriteLinks(p, text, defs[first:])) },
		func(fence string) { b.WriteString(fence) })
	return b.String()
}

// splitFences passes each run of lines outside fenced code, with the index of its first line, to text and each fenced
// block, fences included, to fence.
func splitFences(src string, text func(first int, s string), fence func(string)) {
	lines := strings.SplitAfter(src, "\n")
	start := 0
	for i := 0; i < len(lines); i++ {
		marker := fenceMarker(lines[i])
		if marker == "" {
			continue
		}
		text(start, strings.Join(lines[start:i], ""))
		end := fenceEnd(lines, i, marker)
		fence(strings.Join(lines[i:end+1], ""))
		i, start = end, end+1
	}
	text(start, strings.Join(lines[start:], ""))
}

// fenceMarker returns the backticks or tildes of the fence that the line opens, or "" when it opens none.
func fenceMarker(line string) string {
	m := fenceRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

// definitionLines reports for each line of a page whether a reference definition on it counts: outside fenced code and
// indented code, at most three columns deeper than the content of the list item or admonition that holds it. It
// follows the containers as far as a definition needs: one ends at a line less indented than its content after a blank
// line, or at a line that starts a block of its own; a line indented four columns deeper than its container starts
// indented code after a blank line and continues a paragraph otherwise.
func definitionLines(lines []string) []bool {
	counts := make([]bool, len(lines))
	var open []int // the content columns of the open list items and admonitions, the innermost last
	blank, code := true, false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r\n")
		if line == "" {
			blank = true
			continue
		}
		col, rest := indentation(line)
		marker, content := fenceMarker(line), contentColumn(col, rest)
		if blank || marker != "" || content >= 0 || headingRe.MatchString(rest) {
			for len(open) > 0 && col < open[len(open)-1] {
				open = open[:len(open)-1]
			}
		}
		depth := col
		if len(open) > 0 {
			depth -= open[len(open)-1]
		}
		code = depth >= 4 && (blank || code)
		blank = false
		if marker != "" {
			i, code = fenceEnd(lines, i, marker), false
			continue
		}
		counts[i] = !code
		if depth <= 3 && content >= 0 {
			open = append(open, content)
		}
	}
	return counts
}

// indentation returns the column after the line's leading spaces and tabs, a tab advancing to the next multiple of
// four, and the rest of the line.
func indentation(line string) (int, string) {
	col := 0
	for i := range len(line) {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		default:
			return col, line[i:]
		}
	}
	return col, ""
}

// contentColumn returns the column of the content of the list item or admonition that a line opens at column col,
// whose text after the indentation is rest, or -1 when it opens none.
func contentColumn(col int, rest string) int {
	if strings.HasPrefix(rest, "!!!") || strings.HasPrefix(rest, "???") {
		return col + 4
	}
	m := listItemRe.FindStringSubmatch(rest)
	if m == nil {
		return -1
	}
	spaces := len(m[2])
	if spaces == 0 || spaces > 4 || strings.TrimSpace(rest[len(m[0]):]) == "" {
		spaces = 1
	}
	return col + len(m[1]) + spaces
}

// prose returns the text without its fenced code and code spans, which hold no links.
func prose(src string) string {
	var b strings.Builder
	splitFences(src, func(_ int, text string) {
		for i := 0; i < len(text); {
			switch {
			case text[i] == '\\' && i+1 < len(text):
				b.WriteString(text[i : i+2])
				i += 2
			case text[i] == '`':
				b.WriteByte(' ')
				i = codeSpanAfter(text, i)
			default:
				b.WriteByte(text[i])
				i++
			}
		}
	}, func(string) { b.WriteString("\n") })
	return b.String()
}

// rewriteLinks makes the targets of inline links, images and reference definitions absolute, outside code spans; defs
// tells for each line of s whether a reference definition on it counts.
func (g *generator) rewriteLinks(p *page, s string, defs []bool) string {
	var b strings.Builder
	line, counted := 0, 0
	for i := 0; i < len(s); {
		if i == 0 || s[i-1] == '\n' {
			line += strings.Count(s[counted:i], "\n")
			counted = i
			if m := refDefRe.FindStringSubmatchIndex(s[i:]); m != nil && line < len(defs) && defs[line] {
				lo, hi := m[2], m[3]
				if lo < 0 {
					lo, hi = m[4], m[5]
				}
				b.WriteString(s[i : i+lo])
				b.WriteString(g.absolute(p, s[i+lo:i+hi]))
				i += hi
				continue
			}
		}
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i += 2
		case c == '`':
			end := codeSpanAfter(s, i)
			b.WriteString(s[i:end])
			i = end
		case strings.HasPrefix(s[i:], "]("):
			start := destinationStart(s, i+2)
			b.WriteString(s[i:start])
			i = g.rewriteDestination(&b, p, s, start)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// destinationStart returns the index after the spaces, tabs and at most one line ending that may precede the
// destination of an inline link, from s[i] on.
func destinationStart(s string, i int) int {
	newline := false
	for ; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\r':
		case '\n':
			if newline {
				return i
			}
			newline = true
		default:
			return i
		}
	}
	return i
}

// rewriteDestination writes the destination of an inline link that starts at s[i] and returns the index after it.
func (g *generator) rewriteDestination(b *strings.Builder, p *page, s string, i int) int {
	if i < len(s) && s[i] == '<' {
		end := strings.IndexAny(s[i+1:], ">\n")
		if end < 0 || s[i+1+end] != '>' {
			return i
		}
		b.WriteByte('<')
		b.WriteString(g.absolute(p, s[i+1:i+1+end]))
		b.WriteByte('>')
		return i + end + 2
	}
	j, depth := i, 0
loop:
	for ; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				break loop
			}
			depth--
		case ' ', '\t', '\n':
			break loop
		}
	}
	b.WriteString(g.absolute(p, s[i:j]))
	return j
}

func (g *generator) absolute(p *page, target string) string {
	u, ok := resolveLink(g.base, p.path, target)
	if !ok {
		g.problem("%s: the link %s leaves %s/", path.Join(g.docsDir, p.path), target, g.docsDir)
		return target
	}
	return u
}

// resolveLink makes a relative link target of the page absolute under base and reports false when it leaves the docs
// directory; a target with a scheme, an absolute path or no target stays as it is.
func resolveLink(base, page, target string) (string, bool) {
	switch {
	case target == "" || schemeRe.MatchString(target) || strings.HasPrefix(target, "/"):
		return target, true
	case strings.HasPrefix(target, "#"):
		return base + page + target, true
	}
	file, rest := target, ""
	if k := strings.IndexAny(target, "?#"); k >= 0 {
		file, rest = target[:k], target[k:]
	}
	p := page
	if file != "" {
		p = path.Join(path.Dir(page), file)
		switch {
		case p == ".." || strings.HasPrefix(p, "../"):
			return "", false
		case p == ".":
			p = ""
		case strings.HasSuffix(file, "/"):
			p += "/"
		}
	}
	return base + p + rest, true
}

// render executes the template of llms.txt.
func (g *generator) render(fullBytes int) string {
	src, err := os.ReadFile(g.abs(g.template))
	if err != nil {
		g.problem("%s: %v", g.template, short(err))
		return ""
	}
	t, err := template.New(g.template).Option("missingkey=error").Funcs(template.FuncMap{
		"page":    g.pageURL,
		"spec":    g.specURL,
		"quote":   g.quote,
		"setting": g.setting,
		"enum":    g.enum,
		"map":     g.docMap,
	}).Parse(string(src))
	if err != nil {
		g.problem("%s", strings.TrimPrefix(err.Error(), "template: "))
		return ""
	}
	data := struct {
		Version, Base         string
		FullBytes, FullTokens int
	}{g.version, g.base, fullBytes, fullBytes / bytesPerToken}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		g.problem("%s", strings.TrimPrefix(err.Error(), "template: "))
		return ""
	}
	if g.mapCalls != 1 {
		g.problem("%s: calls map %d times; it must call it once", g.template, g.mapCalls)
	}
	return b.String()
}

// pageURL is the template function page: the URL of a page's Markdown version, with an anchor of its built page.
func (g *generator) pageURL(ref string) string {
	p, anchor, hasAnchor := strings.Cut(ref, "#")
	if _, ok := g.byPath[p]; !ok {
		return g.fail("%s is not a page of the navigation", p)
	}
	if hasAnchor {
		built := path.Join(g.siteDir, builtPage(p))
		ids, err := g.pageIDs(built)
		switch {
		case err != nil:
			return g.fail("%s: %s: %v", ref, built, short(err))
		case !ids[anchor]:
			return g.fail(`%s: %s has no id "%s"`, ref, built, anchor)
		}
	}
	return g.base + ref
}

// builtPage is the HTML file that the build makes of a page, relative to the site.
func builtPage(p string) string {
	if path.Base(p) == "index.md" {
		return path.Join(path.Dir(p), "index.html")
	}
	return strings.TrimSuffix(p, ".md") + "/index.html"
}

func (g *generator) pageIDs(built string) (map[string]bool, error) {
	if ids, ok := g.ids[built]; ok {
		return ids, nil
	}
	data, err := os.ReadFile(g.abs(built))
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, m := range idRe.FindAllStringSubmatch(string(data), -1) {
		ids[m[1]] = true
	}
	g.ids[built] = ids
	return ids, nil
}

// specURL is the template function spec: the URL of a copied specification.
func (g *generator) specURL(name string) string {
	for _, s := range specs {
		if s == name {
			return g.base + s
		}
	}
	return g.fail("%s is no specification: spec takes %s", name, strings.Join(specs, " or "))
}

// quote is the template function quote: the phrase, which the file must contain with runs of white space collapsed.
func (g *generator) quote(file, phrase string) string {
	data, err := g.read(file)
	if err != nil {
		return g.fail("%s: %v", file, err)
	}
	if !strings.Contains(collapse(string(data)), collapse(phrase)) {
		return g.fail(`%s does not say "%s"`, file, phrase)
	}
	return phrase
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// setting is the template function setting: the value of a setting of the defaults, up to the first colon.
func (g *generator) setting(name string) string {
	if g.settings == nil {
		data, err := g.read(g.defaults)
		if err != nil {
			return g.fail("%s: %v", g.defaults, err)
		}
		g.settings = parseSettings(string(data))
	}
	v, ok := g.settings[name]
	if !ok {
		return g.fail("%s has no setting %s", g.defaults, name)
	}
	return v
}

// parseSettings reads the table rows whose first cell is a setting in a code span; the first row of a setting wins.
func parseSettings(src string) map[string]string {
	settings := map[string]string{}
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := tableCells(line)
		if len(cells) < 2 {
			continue
		}
		name, ok := strings.CutPrefix(cells[0], "`")
		name, closed := strings.CutSuffix(name, "`")
		if !ok || !closed || name == "" || strings.Contains(name, "`") {
			continue
		}
		value, _, _ := strings.Cut(cells[1], ":")
		if _, dup := settings[name]; !dup {
			settings[name] = strings.TrimSpace(value)
		}
	}
	return settings
}

// tableCells splits a table row at its unescaped pipes.
func tableCells(row string) []string {
	row = strings.TrimPrefix(row, "|")
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row) && row[i+1] == '|':
			cell.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(row[i])
		}
	}
	if s := strings.TrimSpace(cell.String()); s != "" {
		cells = append(cells, s)
	}
	return cells
}

// enum is the template function enum: the values of a schema's enum, each in a code span, joined by commas.
func (g *generator) enum(file, schema string) string {
	doc, ok := g.specDocs[file]
	if !ok {
		data, err := g.read(file)
		if err != nil {
			return g.fail("%s: %v", file, err)
		}
		doc = &specDoc{}
		if err := yaml.Unmarshal(data, doc); err != nil {
			return g.fail("%s: %v", file, err)
		}
		g.specDocs[file] = doc
	}
	values := doc.Components.Schemas[schema].Enum
	if len(values) == 0 {
		return g.fail("%s has no enum components.schemas.%s", file, schema)
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "`" + v + "`"
	}
	return strings.Join(quoted, ", ")
}

func (g *generator) read(file string) ([]byte, error) {
	if data, ok := g.files[file]; ok {
		return data, nil
	}
	if !fs.ValidPath(file) {
		return nil, errors.New("not a path relative to the repository root")
	}
	data, err := os.ReadFile(g.abs(file))
	if err != nil {
		return nil, short(err)
	}
	g.files[file] = data
	return data, nil
}

// docMap is the template function map: the document map, without its own heading.
func (g *generator) docMap() string {
	g.mapCalls++
	var b strings.Builder
	g.mapItems(&b, g.nav, 3)
	return strings.Trim(b.String(), "\n")
}

// mapItems writes the entries of a level of the navigation: a section under a heading of the level, its sections a
// level lower, and a page that follows a section under a heading of its own, so that it is not read as part of it.
func (g *generator) mapItems(b *strings.Builder, items []*item, level int) {
	afterSection := false
	for _, it := range items {
		if it.page == nil {
			mapHeading(b, level, it.title)
			g.mapItems(b, it.children, level+1)
			afterSection = true
			continue
		}
		if afterSection {
			mapHeading(b, level, it.page.title)
		}
		fmt.Fprintf(b, "- [%s](%s%s)", it.page.title, g.base, it.page.path)
		if it.page.description != "" {
			fmt.Fprintf(b, ": %s", it.page.description)
		}
		b.WriteString("\n")
	}
}

func mapHeading(b *strings.Builder, level int, title string) {
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s %s\n\n", strings.Repeat("#", level), title)
}

// checkLinks checks the URLs of an output: every one into the site must resolve under it, and outside code, which
// holds no links, llms.txt links nowhere else than to the repositories of muster-io, and never by a relative target.
func (g *generator) checkLinks(file, text string, inLLMS bool) {
	for _, u := range urls(text) {
		if strings.HasPrefix(u, g.base) && !g.resolves(strings.TrimPrefix(u, g.base)) {
			g.problem("%s: %s does not resolve under %s/", file, u, g.siteDir)
		}
	}
	if !inLLMS {
		return
	}
	g.checkAbsolute(file, text, "is not absolute")
	for _, u := range urls(prose(text)) {
		if !strings.HasPrefix(u, g.base) && !strings.HasPrefix(u, repositoryPrefix) {
			g.problem("%s: %s leaves the site and is no repository under %s", file, u, repositoryPrefix)
		}
	}
}

// urls returns the distinct URLs of a text in order.
func urls(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range urlRe.FindAllString(text, -1) {
		if u := trimURL(m); !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// trimURL drops the punctuation that follows a URL in text: a sentence's end, a closing bracket or emphasis.
func trimURL(u string) string {
	for u != "" {
		c := u[len(u)-1]
		if !strings.ContainsRune(".,;:!?>]*_", rune(c)) &&
			(c != ')' || strings.Count(u, ")") <= strings.Count(u, "(")) {
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// resolves reports whether a path of the site, after the base URL, names a file under the site; a directory stands
// for its index.html.
func (g *generator) resolves(rest string) bool {
	if k := strings.IndexAny(rest, "?#"); k >= 0 {
		rest = rest[:k]
	}
	rest, err := url.PathUnescape(rest)
	if err != nil {
		return false
	}
	if rest == "" || strings.HasSuffix(rest, "/") {
		rest += "index.html"
	}
	if !fs.ValidPath(rest) {
		return false
	}
	file := g.abs(path.Join(g.siteDir, rest))
	info, err := os.Stat(file)
	if err == nil && info.IsDir() {
		info, err = os.Stat(filepath.Join(file, "index.html"))
	}
	return err == nil && info.Mode().IsRegular()
}

// block is a heading or a paragraph of a page.
type block struct {
	level int    // the level of a heading; 0 for a paragraph
	text  string // the heading's text, or the paragraph's lines trimmed and joined by spaces
}

// blocks returns the headings and paragraphs of a page in order. It leaves out fenced code and HTML comments, and
// every other block with what is indented under it or follows it without a blank line: admonitions, lists, tables,
// block quotes, HTML, images, reference definitions and indented code.
func blocks(src string) []block {
	lines := strings.Split(src, "\n")
	var out []block
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, block{text: strings.Join(para, " ")})
			para = nil
		}
	}
	inOther, afterBlank := false, true
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t\r")
		if line == "" {
			flush()
			afterBlank = true
			continue
		}
		blank := afterBlank
		afterBlank = false
		trimmed := strings.TrimLeft(line, " \t")
		if marker := fenceMarker(line); marker != "" {
			flush()
			i = fenceEnd(lines, i, marker)
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			flush()
			i = commentEnd(lines, i)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			inOther = false
			out = append(out, block{level: len(m[1]), text: headingText(m[2])})
			continue
		}
		indented := trimmed != line
		if inOther && (indented || !blank) {
			continue
		}
		inOther = false
		isRule := ruleRe.MatchString(line)
		if len(para) > 0 && !isRule && !interruptRe.MatchString(line) {
			para = append(para, trimmed)
			continue
		}
		flush()
		switch {
		case isRule:
		case strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") ||
			interruptRe.MatchString(line) || otherRe.MatchString(line):
			inOther = true
		default:
			para = append(para, trimmed)
		}
	}
	flush()
	return out
}

func headingText(s string) string {
	s = strings.TrimSpace(attrRe.ReplaceAllString(s, ""))
	return strings.TrimSpace(hashesRe.ReplaceAllString(s, ""))
}

// fenceEnd returns the line that closes the fence opened at line i with marker, or the last line.
func fenceEnd(lines []string, i int, marker string) int {
	for j := i + 1; j < len(lines); j++ {
		if t := strings.TrimSpace(lines[j]); strings.HasPrefix(t, marker) && strings.Trim(t, marker[:1]) == "" {
			return j
		}
	}
	return len(lines) - 1
}

// commentEnd returns the line that closes the HTML comment opened at line i, or the last line.
func commentEnd(lines []string, i int) int {
	if strings.Contains(lines[i][strings.Index(lines[i], "<!--")+len("<!--"):], "-->") {
		return i
	}
	for j := i + 1; j < len(lines); j++ {
		if strings.Contains(lines[j], "-->") {
			return j
		}
	}
	return len(lines) - 1
}

// titleAndDescription returns the text of the page's first # heading, and the first sentence of the first paragraph
// after it, or of the page when it has no # heading, both with links reduced to their text.
func titleAndDescription(src string) (title, description string) {
	bs := blocks(src)
	start := 0
	for i, b := range bs {
		if b.level == 1 {
			title, start = plainLinks(b.text), i+1
			break
		}
	}
	for _, b := range bs[start:] {
		if b.level == 0 {
			return title, plainLinks(firstSentence(b.text))
		}
	}
	return title, ""
}

// firstSentence ends a paragraph after the first '.', '!' or '?' that white space or the end follows, outside code
// spans and link destinations; a paragraph without one is a sentence.
func firstSentence(p string) string {
	for i := 0; i < len(p); {
		switch c := p[i]; {
		case c == '\\':
			i += 2
		case c == '`':
			i = codeSpanAfter(p, i)
		case strings.HasPrefix(p[i:], "]("):
			end := parenEnd(p, i+1)
			if end < 0 {
				i++
				continue
			}
			i = end
		case c == '.' || c == '!' || c == '?':
			if i+1 == len(p) || p[i+1] == ' ' || p[i+1] == '\t' {
				return p[:i+1]
			}
			i++
		default:
			i++
		}
	}
	return p
}

// plainLinks reduces links to their text and drops images, outside code spans.
func plainLinks(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		image := c == '!' && strings.HasPrefix(s[i+1:], "[")
		switch {
		case c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i += 2
		case c == '`':
			end := codeSpanAfter(s, i)
			b.WriteString(s[i:end])
			i = end
		case c == '[' || image:
			open := i
			if image {
				open++
			}
			text, next, ok := linkAt(s, open)
			if !ok {
				b.WriteByte(c)
				i++
				continue
			}
			if !image {
				b.WriteString(plainLinks(text))
			}
			i = next
		default:
			b.WriteByte(c)
			i++
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// linkAt reads the link whose text opens at s[open] == '[': an inline link [text](target) or a reference link
// [text][ref]. It returns the text and the index after the link.
func linkAt(s string, open int) (string, int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '`':
			i = codeSpanAfter(s, i) - 1
		case '[':
			depth++
		case ']':
			depth--
			if depth > 0 {
				continue
			}
			text := s[open+1 : i]
			switch {
			case strings.HasPrefix(s[i+1:], "("):
				if end := parenEnd(s, i+1); end >= 0 {
					return text, end, true
				}
			case strings.HasPrefix(s[i+1:], "["):
				if end := strings.IndexByte(s[i+2:], ']'); end >= 0 {
					return text, i + 2 + end + 1, true
				}
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

// parenEnd returns the index after the parenthesis that closes the one at s[open], or -1.
func parenEnd(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// codeSpanAfter returns the index after the code span whose backticks start at s[i], or after the backticks when no
// run of the same length closes them before a blank line, which a code span never crosses.
func codeSpanAfter(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	for j := i + n; j < len(s); {
		if s[j] == '\n' && blankLineAt(s, j+1) {
			break
		}
		if s[j] != '`' {
			j++
			continue
		}
		k := j
		for k < len(s) && s[k] == '`' {
			k++
		}
		if k-j == n {
			return k
		}
		j = k
	}
	return i + n
}

// blankLineAt reports whether the line that starts at s[i] holds nothing but white space.
func blankLineAt(s string, i int) bool {
	for ; i < len(s) && s[i] != '\n'; i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			return false
		}
	}
	return true
}
