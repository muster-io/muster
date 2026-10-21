// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command chartannotate writes the Artifact Hub annotations of one release into the Chart.yaml of a copy of the chart,
// before release.yml packages it:
//
//   - artifacthub.io/images: the image of the release, its tag pinned to the digest of its index, with the platforms
//     of the index, read from the JSON that `docker buildx imagetools inspect --format '{{json .Manifest}}'` prints;
//     attestation manifests (unknown/unknown) are not platforms;
//   - artifacthub.io/changes: the entries of the release's section of CHANGELOG.md, as release-please writes it, with
//     a kind from their section and links to their pull request and commit;
//   - artifacthub.io/containsSecurityUpdates: "true" when an entry is a security change;
//   - artifacthub.io/prerelease: "true" for a tag with a pre-release suffix, such as v1.4.0-rc.1, "false" otherwise.
//
// A missing changelog, a missing section or a section without entries is a warning: the chart is published without
// changes. A tag that is not a version, an unreadable index or chart, and an index without a digest fail.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	annotationImages     = "artifacthub.io/images"
	annotationChanges    = "artifacthub.io/changes"
	annotationSecurity   = "artifacthub.io/containsSecurityUpdates"
	annotationPrerelease = "artifacthub.io/prerelease"
	imageName            = "muster"
	attestationReference = "vnd.docker.reference.type"
)

var (
	tagRe = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
		`(-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// The heading of a version: "## [1.2.3](compare-url) (date)", "### [1.2.4](compare-url) (date)" for a patch
	// release, or "## 1.2.3 (date)" for the first release.
	versionHeadingRe = regexp.MustCompile(`^#{2,3} \[?v?([0-9]+\.[0-9]+\.[0-9]+[^\]\s(]*)\]?`)
	entryRe          = regexp.MustCompile(`^[*-] (.*)$`)
	scopeRe          = regexp.MustCompile(`^\*\*([^*:]+):\*\* *(.*)$`)
	linkRe           = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	// The references release-please appends: " ([#123](url))", " ([abc1234](url))", ", closes [#45](url) [#46](url)".
	trailerRe = regexp.MustCompile(`(?:\s*\(\[(?:#[0-9]+|[0-9a-f]{7,40})\]\([^)\s]+\)\)|` +
		`,?\s+closes\s+\[#[0-9]+\]\([^)\s]+\)|\s+\[#[0-9]+\]\([^)\s]+\))+\s*$`)
	commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	cveRe    = regexp.MustCompile(`\bCVE-[0-9]{4}-[0-9]{4,}\b`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chartannotate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	chart := flags.String("chart", "", "the directory of the chart copy whose Chart.yaml is changed (required)")
	tag := flags.String("tag", "", "the release tag, such as v1.2.3 (required)")
	changelog := flags.String("changelog", "CHANGELOG.md", "the changelog that release-please writes")
	image := flags.String("image", "", "the image repository, such as ghcr.io/muster-io/muster (required)")
	index := flags.String("index", "", "the JSON of docker buildx imagetools inspect --format '{{json .Manifest}}' "+
		"of the image's release tag (required)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *chart == "" || *tag == "" || *image == "" || *index == "" || flags.NArg() > 0 {
		fmt.Fprintln(stderr, "chartannotate: -chart, -tag, -image and -index are required, and nothing else is an argument")
		return 2
	}
	if err := annotate(*chart, *tag, *changelog, *image, *index, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "chartannotate: %v\n", err)
		return 1
	}
	return 0
}

func annotate(chartDir, tag, changelogPath, image, indexPath string, stdout, stderr io.Writer) error {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return fmt.Errorf("the tag %q is not a version such as v1.2.3", tag)
	}
	version := strings.TrimPrefix(tag, "v")
	prerelease := m[4] != ""

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("reading the image index: %w", err)
	}
	img, err := parseIndex(data)
	if err != nil {
		return fmt.Errorf("%s: %w", indexPath, err)
	}
	img.Name = imageName
	img.Image = image + ":" + tag + "@" + img.digest

	// An empty value removes the annotation, so that a chart annotated before keeps no stale changes.
	annotations := map[string]string{annotationPrerelease: fmt.Sprint(prerelease), annotationChanges: "",
		annotationSecurity: ""}
	images, err := yamlString([]imageEntry{img})
	if err != nil {
		return err
	}
	annotations[annotationImages] = images

	changes, warning := readChanges(changelogPath, version)
	if warning != "" {
		fmt.Fprintf(stderr, "chartannotate: warning: %s; the chart is published without %s\n", warning, annotationChanges)
	} else {
		s, err := yamlString(changes)
		if err != nil {
			return err
		}
		annotations[annotationChanges] = s
		if slices.ContainsFunc(changes, func(c change) bool { return c.Kind == kindSecurity }) {
			annotations[annotationSecurity] = "true"
		}
	}

	chartFile := filepath.Join(chartDir, "Chart.yaml")
	if err := setAnnotations(chartFile, annotations); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: image %s (%s), %d changes, prerelease %t\n",
		chartFile, img.Image, strings.Join(img.Platforms, ", "), len(changes), prerelease)
	return nil
}

// imageEntry is one entry of artifacthub.io/images.
type imageEntry struct {
	Name      string   `yaml:"name"`
	Image     string   `yaml:"image"`
	Platforms []string `yaml:"platforms,omitempty"`
	digest    string
}

// parseIndex reads the digest of the index and the platforms of its image manifests.
func parseIndex(data []byte) (imageEntry, error) {
	var idx struct {
		Digest    string `json:"digest"`
		Manifests []struct {
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return imageEntry{}, fmt.Errorf("not the JSON of an image index: %w", err)
	}
	if !digestRe.MatchString(idx.Digest) {
		return imageEntry{}, fmt.Errorf("the index has no sha256 digest, but %q", idx.Digest)
	}
	e := imageEntry{digest: idx.Digest}
	for _, mf := range idx.Manifests {
		p := mf.Platform
		if p == nil || p.OS == "" || p.OS == "unknown" || p.Architecture == "unknown" || mf.Annotations[attestationReference] != "" {
			continue
		}
		platform := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			platform += "/" + p.Variant
		}
		if !slices.Contains(e.Platforms, platform) {
			e.Platforms = append(e.Platforms, platform)
		}
	}
	slices.Sort(e.Platforms)
	return e, nil
}

const (
	kindAdded    = "added"
	kindChanged  = "changed"
	kindFixed    = "fixed"
	kindSecurity = "security"
	kindSkip     = ""

	breakingPrefix = "Breaking: "
)

// change is one entry of artifacthub.io/changes in its structured form.
type change struct {
	Kind        string `yaml:"kind"`
	Description string `yaml:"description"`
	Links       []link `yaml:"links,omitempty"`
}

type link struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// sectionKind maps a section of release-please's changelog to a kind of change. Documentation describes the
// documentation site, not the chart or the image, and the sections release-please hides by default are internal.
func sectionKind(title string) (kind, prefix string) {
	t := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(title, "⚠")))
	switch {
	case strings.Contains(t, "security"):
		return kindSecurity, ""
	case t == "features":
		return kindAdded, ""
	case t == "bug fixes":
		return kindFixed, ""
	case strings.Contains(t, "breaking"):
		return kindChanged, breakingPrefix
	case t == "documentation", t == "miscellaneous chores", t == "chores", t == "tests", t == "build system",
		t == "continuous integration", t == "styles":
		return kindSkip, ""
	default:
		return kindChanged, ""
	}
}

// readChanges returns the changes of the version's section, or a warning that says why there are none.
func readChanges(path, version string) ([]change, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("cannot read the changelog: %v", err)
	}
	changes, found := parseChangelog(data, version)
	switch {
	case !found:
		return nil, fmt.Sprintf("%s has no section for %s", path, version)
	case len(changes) == 0:
		return nil, fmt.Sprintf("the section for %s in %s has no entries", version, path)
	}
	return changes, ""
}

// entry is one list item of a changelog section with the kind and the prefix of its section.
type entry struct {
	text, kind, prefix string
}

// parseChangelog reads the entries of the section of one version. Every other non-blank line that is not a heading
// continues the entry above it, such as the text of a breaking change. A feature or fix that is also listed as a
// breaking change with the same text is kept once, as the breaking change.
func parseChangelog(data []byte, version string) (changes []change, found bool) {
	var (
		in      bool
		kind    string
		prefix  string
		entries []entry
	)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := versionHeadingRe.FindStringSubmatch(line); m != nil || strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			if m != nil && m[1] == version {
				in, found = true, true
				kind = kindChanged
			}
			continue
		}
		if !in {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#"):
			kind, prefix = sectionKind(strings.TrimLeft(line, "# "))
			entries = append(entries, entry{}) // ends the entry above, so the next lines do not continue it
		case kind == kindSkip:
		case entryRe.MatchString(line):
			entries = append(entries, entry{text: entryRe.FindStringSubmatch(line)[1], kind: kind, prefix: prefix})
		case strings.TrimSpace(line) != "" && len(entries) > 0 && entries[len(entries)-1].text != "":
			entries[len(entries)-1].text += " " + strings.TrimSpace(line)
		}
	}
	breaking := map[string]bool{}
	for _, e := range entries {
		if c, ok := parseEntry(e); ok {
			changes = append(changes, c)
			if e.prefix != "" {
				breaking[strings.TrimPrefix(c.Description, e.prefix)] = true
			}
		}
	}
	changes = slices.DeleteFunc(changes, func(c change) bool {
		return !strings.HasPrefix(c.Description, breakingPrefix) && breaking[c.Description]
	})
	return changes, found
}

// parseEntry turns "**scope:** text ([#1](url)) ([abc1234](url))" into "scope: text" with links to #1 and abc1234. A
// link in the text stays as its text and becomes a link too. A security scope or a CVE identifier makes it a security
// change.
func parseEntry(e entry) (change, bool) {
	text := e.text
	trailer := ""
	if loc := trailerRe.FindStringIndex(text); loc != nil {
		text, trailer = text[:loc[0]], text[loc[0]:]
	}
	scope := ""
	if m := scopeRe.FindStringSubmatch(text); m != nil {
		scope, text = strings.TrimSpace(m[1]), m[2]
	}
	var links []link
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		links = append(links, link{Name: m[1], URL: m[2]})
	}
	for _, m := range linkRe.FindAllStringSubmatch(trailer, -1) {
		name := m[1]
		if commitRe.MatchString(name) {
			name = "Commit " + name
		}
		links = append(links, link{Name: name, URL: m[2]})
	}
	text = linkRe.ReplaceAllString(text, "$1")
	text = strings.TrimSpace(strings.ReplaceAll(text, "**", ""))
	if text == "" {
		return change{}, false
	}
	kind := e.kind
	if strings.EqualFold(scope, "security") || cveRe.MatchString(text) {
		kind = kindSecurity
	}
	if scope != "" {
		text = scope + ": " + text
	}
	return change{Kind: kind, Description: e.prefix + text, Links: links}, true
}

func yamlString(v any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// setAnnotations sets the annotations in Chart.yaml and removes those whose value is empty, through its node tree, so
// that every other key, its order and its comments stay as they are.
func setAnnotations(path string, annotations map[string]string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: not a YAML mapping", path)
	}
	root := doc.Content[0]
	ann := mappingValue(root, "annotations")
	if ann == nil {
		ann = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "annotations"}, ann)
	}
	if ann.Kind != yaml.MappingNode {
		return errors.New(path + ": annotations is not a mapping")
	}
	keys := make([]string, 0, len(annotations))
	for k := range annotations {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if annotations[k] == "" {
			removeKey(ann, k)
			continue
		}
		value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: annotations[k]}
		if strings.Contains(value.Value, "\n") {
			value.Style = yaml.LiteralStyle
		} else {
			value.Style = yaml.DoubleQuotedStyle
		}
		if v := mappingValue(ann, k); v != nil {
			*v = *value
			continue
		}
		ann.Content = append(ann.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, value)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), info.Mode().Perm())
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}
