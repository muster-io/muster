// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"cmp"
	"encoding/base64"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Channel is a channel of an app's workspace, as /_fake/config shows it by its id: its name, whether the app's bot is a
// member, whether it is archived and whether it is private.
type Channel struct {
	Name     string `json:"name"`
	Member   bool   `json:"member"`
	Archived bool   `json:"archived"`
	Private  bool   `json:"private"`
}

// The channels every app has by default: "alerts", with the bot, and "other", without it.
const (
	ChannelAlerts = "C0FAKEALERTS"
	ChannelOther  = "C0FAKEOTHER"
)

// The page sizes of conversations.list: Slack's default and its largest.
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// cursorPrefix starts every cursor of conversations.list before base64, followed by the id the next page starts at.
const cursorPrefix = "next:"

// The scopes that let a token read each type of conversation (F-070, F-071).
var typeScopes = map[string]string{"public_channel": "channels:read", "private_channel": "groups:read",
	"mpim": "mpim:read", "im": "im:read"}

// defaultChannels are the channels of a new app: ChannelAlerts "alerts" with the bot and ChannelOther "other" without.
func defaultChannels() map[string]Channel {
	return map[string]Channel{ChannelAlerts: {Name: "alerts", Member: true}, ChannelOther: {Name: "other"}}
}

// channelAnswer is a channel as conversations.info and conversations.list answer it (F-098).
func channelAnswer(id string, ch Channel) map[string]any {
	return map[string]any{"id": id, "name": ch.Name, "is_channel": true, "is_group": false, "is_im": false,
		"is_private": ch.Private, "is_archived": ch.Archived, "is_member": ch.Member}
}

// missingScope answers missing_scope with the scope needed and those the token has (F-070).
func missingScope(w http.ResponseWriter, needed, scopes string) {
	writeJSON(w, map[string]any{"ok": false, "error": "missing_scope", "needed": needed, "provided": scopes})
}

// conversationsInfo answers conversations.info for the channel id (F-098): an app-level token is the wrong kind; an
// unknown id, a direct chat's included, and a private channel the bot is not in are channel_not_found; a token without
// the scope of the channel's type lacks it; otherwise the channel, ok even when the bot is not a member.
func conversationsInfo(w http.ResponseWriter, c caller, cfg Config, scopes, id string) {
	if c.kind == kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	ch, ok := cfg.Channels[c.app.Key][id]
	if !ok || ch.Private && !ch.Member {
		writeError(w, "channel_not_found")
		return
	}
	needed := typeScopes["public_channel"]
	if ch.Private {
		needed = typeScopes["private_channel"]
	}
	if !slices.Contains(splitScopes(scopes), needed) {
		missingScope(w, needed, scopes)
		return
	}
	writeOK(w, map[string]any{"channel": channelAnswer(id, ch)})
}

// conversationsList answers conversations.list (F-098, F-099) in id order: the public channels of the app, members or
// not, and its private ones the bot is a member of, of the types asked — public_channel by default — without the
// archived ones when exclude_archived is true or 1; a type whose scope the token lacks is missing_scope. A page holds
// limit channels — 100 by default, at most 1000, and at most the page_size of the configuration when it is set — and
// next_cursor names the rest; a cursor the fake did not give is invalid_cursor.
func conversationsList(w http.ResponseWriter, c caller, cfg Config, scopes string, args map[string]string) {
	if c.kind == kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	types := map[string]bool{}
	for t := range strings.SplitSeq(cmp.Or(args["types"], "public_channel"), ",") {
		t = strings.TrimSpace(t)
		scope, known := typeScopes[t]
		switch {
		case !known:
			writeError(w, "invalid_types")
			return
		case !slices.Contains(splitScopes(scopes), scope):
			missingScope(w, scope, scopes)
			return
		}
		types[t] = true
	}
	excludeArchived := args["exclude_archived"] == "true" || args["exclude_archived"] == "1"
	channels := cfg.Channels[c.app.Key]
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(channels)) {
		ch := channels[id]
		switch {
		case excludeArchived && ch.Archived:
		case ch.Private && types["private_channel"] && ch.Member, !ch.Private && types["public_channel"]:
			ids = append(ids, id)
		}
	}
	start := 0
	if cursor := args["cursor"]; cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(cursor)
		from, ok := strings.CutPrefix(string(raw), cursorPrefix)
		if err != nil || !ok || from == "" {
			writeError(w, "invalid_cursor")
			return
		}
		start, _ = slices.BinarySearch(ids, from)
	}
	limit := defaultListLimit
	if n, err := strconv.Atoi(args["limit"]); err == nil && n > 0 {
		limit = min(n, maxListLimit)
	}
	if cfg.PageSize > 0 {
		limit = min(limit, cfg.PageSize)
	}
	end := min(start+limit, len(ids))
	page := make([]map[string]any, 0, end-start)
	for _, id := range ids[start:end] {
		page = append(page, channelAnswer(id, channels[id]))
	}
	next := ""
	if end < len(ids) {
		next = base64.StdEncoding.EncodeToString([]byte(cursorPrefix + ids[end]))
	}
	writeOK(w, map[string]any{"channels": page, "response_metadata": map[string]any{"next_cursor": next}})
}

// cloneChannels copies the channels of each app.
func cloneChannels(in map[string]map[string]Channel) map[string]map[string]Channel {
	out := make(map[string]map[string]Channel, len(in))
	for key, chans := range in {
		out[key] = maps.Clone(chans)
		if out[key] == nil {
			out[key] = map[string]Channel{}
		}
	}
	return out
}
