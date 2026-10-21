// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command docslinkcheck fails when a link of the web UI points at a page or a section that the built documentation site
// lacks (C-21.FR-12). It reads the list of the web UI's links (-links, web/src/lib/docs-links.json), an object from key
// to target `<path>.md` or `<path>.md#<anchor>`, and the built site (-site, site): for each entry the page
// site/<path>/index.html, or site/<dir>/index.html for an index.md, must exist and, for an anchor, hold an element
// whose id is the anchor. It prints one line per problem to stderr, naming the key and the target, and exits with 1;
// a site that is not built is one problem. Otherwise it prints the number of entries checked. make docs runs it after
// the build.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("docslinkcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	linksFile := flags.String("links", "web/src/lib/docs-links.json", "the list of the web UI's documentation links")
	site := flags.String("site", "site", "the built documentation site")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	links, err := readLinks(*linksFile)
	if err != nil {
		fmt.Fprintf(stderr, "docslinkcheck: %v\n", err)
		return 1
	}
	if info, err := os.Stat(*site); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "site not built: %s\n", *site)
		return 1
	}
	problems, err := check(links, *site)
	if err != nil {
		fmt.Fprintf(stderr, "docslinkcheck: %v\n", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, p)
		}
		fmt.Fprintf(stderr, "Change the entry of %s to a page and a heading the site has, or restore the heading.\n",
			*linksFile)
		return 1
	}
	fmt.Fprintf(stdout, "%d documentation links of the web UI: every page and anchor exists in %s\n", len(links), *site)
	return 0
}

func readLinks(file string) (map[string]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var links map[string]string
	if err := json.Unmarshal(data, &links); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("%s holds no link", file)
	}
	return links, nil
}

// check returns one problem per entry whose page or anchor the site lacks, in the order of the keys.
func check(links map[string]string, site string) ([]string, error) {
	pages := map[string]map[string]bool{}
	var problems []string
	for _, key := range slices.Sorted(maps.Keys(links)) {
		target := links[key]
		file, anchor, _ := strings.Cut(target, "#")
		page, ok := pagePath(file)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s: not a page of docs/ (<path>.md or <path>.md#<anchor>)",
				key, target))
			continue
		}
		built := filepath.Join(site, filepath.FromSlash(page))
		ids, seen := pages[built]
		if !seen {
			data, err := os.ReadFile(built)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				ids = nil
			case err != nil:
				return nil, err
			default:
				ids = elementIDs(string(data))
			}
			pages[built] = ids
		}
		switch {
		case ids == nil:
			problems = append(problems, fmt.Sprintf("%s: %s: the site has no page %s", key, target, built))
		case anchor != "" && !ids[anchor]:
			problems = append(problems, fmt.Sprintf("%s: %s: no element with this id in %s", key, target, built))
		}
	}
	return problems, nil
}

// pagePath is the built page of a source page: a/b.md is a/b/index.html, a/index.md is a/index.html.
func pagePath(file string) (string, bool) {
	if !strings.HasSuffix(file, ".md") || strings.HasPrefix(file, "/") || strings.Contains(file, "\\") ||
		path.Clean(file) != file || strings.HasPrefix(file, "../") {
		return "", false
	}
	page := strings.TrimSuffix(file, ".md")
	if page == "index" || strings.HasSuffix(page, "/index") {
		return page + ".html", true
	}
	return page + "/index.html", true
}

// elementIDs collects the id attributes of the tags of a page, with their values quoted with " or ', or unquoted,
// and their character references decoded. Comments, and the text of script and style elements, hold no tags.
func elementIDs(doc string) map[string]bool {
	ids := map[string]bool{}
	for i := 0; i < len(doc); {
		lt := strings.IndexByte(doc[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		rest := doc[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				return ids
			}
			i += 4 + end + 3
			continue
		case len(rest) < 2 || !isLetter(rest[1]):
			i++
			continue
		}
		name, attrs, next := readTag(doc, i+1)
		for _, a := range attrs {
			if a[0] == "id" && a[1] != "" {
				ids[a[1]] = true
			}
		}
		i = next
		if name == "script" || name == "style" {
			end := strings.Index(strings.ToLower(doc[i:]), "</"+name)
			if end < 0 {
				return ids
			}
			i += end
		}
	}
	return ids
}

// readTag reads the tag whose name starts at i: its name and attributes in lower case, the values decoded, and the
// index after its '>'.
func readTag(doc string, i int) (string, [][2]string, int) {
	start := i
	for i < len(doc) && !isSpace(doc[i]) && doc[i] != '>' && doc[i] != '/' {
		i++
	}
	name := strings.ToLower(doc[start:i])
	var attrs [][2]string
	for i < len(doc) {
		for i < len(doc) && (isSpace(doc[i]) || doc[i] == '/') {
			i++
		}
		if i >= len(doc) {
			break
		}
		if doc[i] == '>' {
			return name, attrs, i + 1
		}
		start = i
		for i < len(doc) && !isSpace(doc[i]) && doc[i] != '>' && doc[i] != '=' && doc[i] != '/' {
			i++
		}
		attr := strings.ToLower(doc[start:i])
		for i < len(doc) && isSpace(doc[i]) {
			i++
		}
		value := ""
		if i < len(doc) && doc[i] == '=' {
			i++
			for i < len(doc) && isSpace(doc[i]) {
				i++
			}
			if i < len(doc) && (doc[i] == '"' || doc[i] == '\'') {
				quote := doc[i]
				end := strings.IndexByte(doc[i+1:], quote)
				if end < 0 {
					return name, attrs, len(doc)
				}
				value = doc[i+1 : i+1+end]
				i += end + 2
			} else {
				start = i
				for i < len(doc) && !isSpace(doc[i]) && doc[i] != '>' {
					i++
				}
				value = doc[start:i]
			}
		}
		if attr != "" {
			attrs = append(attrs, [2]string{attr, html.UnescapeString(value)})
		} else if i < len(doc) && doc[i] != '>' {
			i++
		}
	}
	return name, attrs, len(doc)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
