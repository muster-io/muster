// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command imagecheck fails when an image of the documentation site is broken. It reads every Markdown page under the
// given directory (docs by default), except the files that exclude_docs of the site's configuration (-config,
// mkdocs.yml by default) leaves out, collects the images outside code — `![alt](path)` and `<img src alt>` — and prints
// one line per problem, naming the page, the line and the image: a relative path that does not resolve to a file under
// the directory, an image without alternative text, and an image whose source is another site (http:, https: or //),
// which a reader's browser would load from that site. After reading every page it names each PNG under
// assets/screenshots/ that no page references, the output of make docs-screenshots that a page stopped using. It
// exits with 1 on a problem; make docs-check runs it before the build. A reference-style image — ![alt][ref], ![alt][]
// or ![ref], whose path a [ref]: definition gives — fails too, asking for an inline image, which the check can follow.
// References are matched line by line: an image whose alternative text wraps onto the next line and an <img> tag over
// several lines are not read.
package main

import (
	"bufio"
	"bytes"
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
)

// screenshots is the directory, under the documentation, of the images that make docs-screenshots writes.
const screenshots = "assets/screenshots"

var (
	// markdownImage is `![alt](target)`; the target is a path, optionally in <>, and an optional title.
	markdownImage = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(<[^>]*>|[^)\s]*)(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)`)
	// referenceImage is an image left once the inline ones are gone: ![alt][ref], ![alt][] or ![ref].
	referenceImage = regexp.MustCompile(`!\[[^\]]*\](?:\[[^\]]*\])?`)
	imgTag         = regexp.MustCompile(`(?i)<img\b[^>]*>`)
	imgAttr        = regexp.MustCompile(`(?i)\b(src|alt)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	remote         = regexp.MustCompile(`(?i)^(https?:|//)`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("imagecheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := flags.String("config", "mkdocs.yml", "the site's configuration, whose exclude_docs names the files to skip")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	root := "docs"
	if flags.NArg() > 0 {
		root = flags.Arg(0)
	}
	excluded, err := excludeDocs(*config)
	if err != nil {
		fmt.Fprintf(stderr, "imagecheck: %v\n", err)
		return 1
	}
	problems, images, pages, err := check(root, excluded)
	if err != nil {
		fmt.Fprintf(stderr, "imagecheck: %v\n", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p)
		}
		fmt.Fprintln(stdout, "Give every image a file under "+root+"/ and alternative text that says what it shows, "+
			"and remove a screenshot that no page uses, or run make docs-screenshots again.")
		return 1
	}
	fmt.Fprintf(stdout, "%d images on %d pages: every one exists, has alternative text and comes from the site, and "+
		"every screenshot is referenced\n", images, pages)
	return 0
}

// excludeDocs reads the patterns of exclude_docs, a block scalar in the site's configuration.
func excludeDocs(config string) ([]string, error) {
	content, err := os.ReadFile(config)
	if err != nil {
		return nil, err
	}
	var patterns []string
	in := false
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "exclude_docs:"):
			in = true
		case in && strings.TrimSpace(line) == "":
		case in && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			if p := strings.TrimSpace(line); !strings.HasPrefix(p, "#") {
				patterns = append(patterns, p)
			}
		default:
			in = false
		}
	}
	return patterns, sc.Err()
}

// isExcluded reports whether the page at name, a slash-separated path under the documentation, matches one of the
// patterns as exclude_docs reads them: a pattern ending in / names a directory; one with a / elsewhere is anchored at
// the root; any other matches a file or directory of that name at any level.
func isExcluded(name string, patterns []string) bool {
	parts := strings.Split(name, "/")
	for _, p := range patterns {
		dir := strings.HasSuffix(p, "/")
		p = strings.Trim(p, "/")
		if strings.Contains(p, "/") {
			for i := range parts {
				if ok, _ := path.Match(p, strings.Join(parts[:i+1], "/")); ok && (i < len(parts)-1 || !dir) {
					return true
				}
			}
			continue
		}
		for i, part := range parts {
			if ok, _ := path.Match(p, part); ok && (i < len(parts)-1 || !dir) {
				return true
			}
		}
	}
	return false
}

// image is one image reference of a page.
type image struct {
	line     int
	src, alt string
	// reference is the text of a reference-style image, which has no inline path.
	reference string
}

// check reads the pages under root in lexical order and returns their problems, by page and line, then the
// screenshots no page references, and how many images and pages it read.
func check(root string, excluded []string) ([]string, int, int, error) {
	fsys := os.DirFS(root)
	var problems []string
	used := map[string]bool{}
	images, pages := 0, 0
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".md" || isExcluded(name, excluded) {
			return err
		}
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		pages++
		page := filepath.Join(root, filepath.FromSlash(name))
		for _, img := range references(string(content)) {
			images++
			problems = append(problems, problemsOf(fsys, page, name, img, used)...)
		}
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	err = fs.WalkDir(fsys, screenshots, func(name string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && name == screenshots {
			return fs.SkipDir
		}
		if err != nil || d.IsDir() || !strings.EqualFold(path.Ext(name), ".png") || used[name] {
			return err
		}
		problems = append(problems, fmt.Sprintf("%s is referenced by no page", filepath.Join(root,
			filepath.FromSlash(name))))
		return nil
	})
	return problems, images, pages, err
}

// problemsOf checks one image of the page at name, page being its path as printed, and marks the file it resolves to
// as used.
func problemsOf(fsys fs.FS, page, name string, img image, used map[string]bool) []string {
	var out []string
	at := fmt.Sprintf("%s:%d: ", page, img.line)
	if img.reference != "" {
		return append(out, at+img.reference+" is a reference-style image; write it inline, ![alt](path), so that the "+
			"check can follow it")
	}
	if strings.TrimSpace(img.alt) == "" {
		out = append(out, at+img.src+" has no alternative text")
	}
	if remote.MatchString(img.src) {
		return append(out, at+img.src+" is on another site; keep the image under the documentation")
	}
	if strings.HasPrefix(strings.ToLower(img.src), "data:") {
		return out
	}
	target := img.src
	if i := strings.IndexAny(target, "?#"); i >= 0 {
		target = target[:i]
	}
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	var file string
	if strings.HasPrefix(target, "/") {
		file = path.Clean(strings.TrimPrefix(target, "/"))
	} else {
		file = path.Join(path.Dir(name), target)
	}
	if target == "" || !fs.ValidPath(file) {
		return append(out, at+img.src+" does not exist")
	}
	info, err := fs.Stat(fsys, file)
	if err != nil || info.IsDir() {
		return append(out, at+img.src+" does not exist")
	}
	used[file] = true
	return out
}

// references are the images of a Markdown page outside fenced code blocks and inline code, in order.
func references(content string) []image {
	var out []image
	fence := ""
	for i, line := range strings.Split(content, "\n") {
		if fence = fenceState(fence, line); fence != "" || isFence(line) {
			continue
		}
		line = withoutCode(line)
		for _, m := range markdownImage.FindAllStringSubmatch(line, -1) {
			out = append(out, image{line: i + 1, alt: m[1], src: strings.Trim(m[2], "<>")})
		}
		for _, ref := range referenceImage.FindAllString(markdownImage.ReplaceAllString(line, ""), -1) {
			out = append(out, image{line: i + 1, reference: ref})
		}
		for _, tag := range imgTag.FindAllString(line, -1) {
			img := image{line: i + 1}
			for _, a := range imgAttr.FindAllStringSubmatch(tag, -1) {
				v := strings.Trim(a[2], `"'`)
				if strings.EqualFold(a[1], "src") {
					img.src = v
				} else {
					img.alt = v
				}
			}
			out = append(out, img)
		}
	}
	return out
}

// fenceState returns the code fence that is open after line, given the one open before it; "" is none.
func fenceState(open, line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if open != "" {
		if strings.HasPrefix(trimmed, open) && strings.Trim(trimmed, open[:1]+" \t") == "" {
			return ""
		}
		return open
	}
	for _, c := range []byte{'`', '~'} {
		if run := leadingRun(trimmed, c); len(run) >= 3 {
			return run
		}
	}
	return ""
}

// isFence reports whether line opens or closes a code fence.
func isFence(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return len(leadingRun(trimmed, '`')) >= 3 || len(leadingRun(trimmed, '~')) >= 3
}

func leadingRun(s string, c byte) string {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return s[:n]
}

// withoutCode is line with its inline code spans removed: a run of backticks opens a span that the next run of the
// same length closes.
func withoutCode(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		open := leadingRun(line[i:], '`')
		end := closing(line, i+len(open), len(open))
		if end < 0 {
			b.WriteString(open)
			i += len(open)
			continue
		}
		i = end + len(open)
	}
	return b.String()
}

// closing returns the index of the next run of exactly n backticks in line at or after from, or -1.
func closing(line string, from, n int) int {
	for j := from; j < len(line); {
		if line[j] != '`' {
			j++
			continue
		}
		run := leadingRun(line[j:], '`')
		if len(run) == n {
			return j
		}
		j += len(run)
	}
	return -1
}
