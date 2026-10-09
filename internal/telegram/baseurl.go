// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"errors"
	"net/url"
	"strings"
)

// DefaultBaseURL is connection.telegram.bot_api_base_url: Telegram's cloud Bot API.
const DefaultBaseURL = "https://api.telegram.org"

// WarningBaseURLUsesHTTP is the warning of a Connection whose Bot API base URL is http (C-14.FR-10): the bot token
// travels unencrypted.
const WarningBaseURLUsesHTTP = "base_url_uses_http"

// ErrBaseURL is a Bot API base URL that breaks the rules of C-14.FR-10.
var ErrBaseURL = errors.New("the Bot API base URL is not an absolute http or https URL without user information, " +
	"query or fragment")

// ParseBaseURL checks a Bot API base URL (C-14.FR-10) and returns it normalized: an absolute http or https URL with a
// host and without user information, query or fragment, whose path prefix is kept without its trailing slashes, so
// that requests go to `<base>/bot<token>/<method>`.
func ParseBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "?#") {
		return "", ErrBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" {
		return "", ErrBaseURL
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

// BaseURLWarnings are the warnings of a normalized base URL: WarningBaseURLUsesHTTP for http.
func BaseURLWarnings(base string) []string {
	if strings.HasPrefix(base, "http://") {
		return []string{WarningBaseURLUsesHTTP}
	}
	return []string{}
}

// Origin is what log lines show of a base URL (C-14.FR-12, C-02.FR-20): its scheme and host only, never its path
// prefix, which may be a secret of a reverse proxy.
func Origin(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
