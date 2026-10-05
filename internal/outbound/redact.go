// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"net/url"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/logging"
)

const redacted = "[redacted]"

// redactor replaces registered secrets with [redacted], verbatim and in the escaped forms a URL gives them.
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
		for _, f := range []string{raw, url.PathEscape(raw), url.QueryEscape(raw), (&url.URL{Path: raw}).EscapedPath()} {
			if !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	// Longer forms first, so that a secret contained in another is not replaced inside it first.
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return redactor{forms: forms}
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
