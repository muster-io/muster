// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/logging"
)

const redacted = "[redacted]"

// redactor replaces registered secrets with [redacted], verbatim and in the escaped forms a URL or a JSON string gives
// them.
type redactor struct {
	forms []string
}

func newRedactor(secrets []logging.Secret) redactor {
	var forms []string
	for _, s := range secrets {
		if s == "" {
			continue
		}
		raw := string(s)
		candidates := []string{raw, url.PathEscape(raw), url.QueryEscape(raw), (&url.URL{Path: raw}).EscapedPath()}
		for _, f := range append(candidates, jsonForms(raw)...) {
			if f != "" && !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	// Longer forms first, so that a secret contained in another is not replaced inside it first.
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return redactor{forms: forms}
}

// jsonForms are raw as a JSON string holds it, without its quotes: escaped as encoding/json escapes it, with and
// without its HTML escaping of <, > and &, and each with its slashes escaped as well, as other encoders write them.
func jsonForms(raw string) []string {
	var out []string
	for _, html := range []bool{true, false} {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(html)
		_ = enc.Encode(raw) // a string always encodes
		s := strings.TrimSuffix(b.String(), "\n")
		s = s[1 : len(s)-1]
		out = append(out, s, strings.ReplaceAll(s, "/", `\/`))
	}
	return out
}

func (r redactor) redact(s string) string {
	for _, f := range r.forms {
		s = strings.ReplaceAll(s, f, redacted)
	}
	return s
}

// url is u as an error may show it: without user information, with its path and query unescaped and every
// registered secret replaced, however the caller escaped it.
func (r redactor) url(u *url.URL) string {
	s := u.Scheme + "://" + u.Host + r.redact(u.Path)
	if u.RawQuery != "" {
		q := r.redact(u.RawQuery)
		if dq, err := url.PathUnescape(q); err == nil {
			q = r.redact(dq)
		}
		s += "?" + q
	}
	return s
}
