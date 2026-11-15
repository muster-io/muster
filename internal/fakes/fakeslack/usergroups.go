// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"maps"
	"net/http"
	"slices"
)

// UserGroup is a user group of an app's workspace, as /_fake/config shows it by its id: its handle, which a mention
// <!subteam^ID> is drawn as, its name, its people by user id, and whether it is disabled.
type UserGroup struct {
	Handle   string   `json:"handle"`
	Name     string   `json:"name"`
	Users    []string `json:"users"`
	Disabled bool     `json:"disabled"`
}

// The user group every app has by default: "payments-team", with UserAlice (F-125).
const UserGroupPayments = "S0FAKEPAYTEAM"

// usergroupsScope is the scope usergroups.list needs (F-125).
const usergroupsScope = "usergroups:read"

// The times of the user groups of the fake, as usergroups.list answers them.
const (
	userGroupCreated  = 1763200000
	userGroupDisabled = 1763210000
)

// defaultUserGroups are the user groups of a new app: UserGroupPayments "payments-team" with UserAlice.
func defaultUserGroups() map[string]UserGroup {
	return map[string]UserGroup{UserGroupPayments: {Handle: "payments-team", Name: "Payments team",
		Users: []string{UserAlice}}}
}

// cloneUserGroups copies the user groups of each app.
func cloneUserGroups(in map[string]map[string]UserGroup) map[string]map[string]UserGroup {
	out := make(map[string]map[string]UserGroup, len(in))
	for key, groups := range in {
		out[key] = make(map[string]UserGroup, len(groups))
		for id, g := range groups {
			g.Users = slices.Clone(g.Users)
			if g.Users == nil {
				g.Users = []string{}
			}
			out[key][id] = g
		}
	}
	return out
}

// userGroupAnswer is a user group as usergroups.list answers it (F-125): date_delete is 0 unless it is disabled, and
// user_count is there with include_count.
func userGroupAnswer(c caller, id string, g UserGroup, count bool) map[string]any {
	deleted := 0
	if g.Disabled {
		deleted = userGroupDisabled
	}
	out := map[string]any{"id": id, "team_id": c.app.TeamID, "handle": g.Handle, "name": g.Name,
		"description": "", "is_external": false, "date_create": userGroupCreated, "date_update": userGroupCreated,
		"date_delete": deleted, "created_by": c.app.BotUserID, "updated_by": c.app.BotUserID, "deleted_by": nil,
		"prefs": map[string]any{"channels": []string{}, "groups": []string{}}, "channel_count": 0}
	if count {
		out["user_count"] = len(g.Users)
	}
	return out
}

// usergroupsList answers usergroups.list (F-125) in id order: the user groups of the caller's app that are not
// disabled, the disabled ones too with include_disabled, each with user_count when include_count is true or 1. An
// app-level token is the wrong kind, and a token without usergroups:read gets missing_scope with the scope needed
// and those it has.
func usergroupsList(w http.ResponseWriter, c caller, cfg Config, scopes string, args map[string]string) {
	if c.kind == kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	if !slices.Contains(splitScopes(scopes), usergroupsScope) {
		missingScope(w, usergroupsScope, scopes)
		return
	}
	truth := func(v string) bool { return v == "true" || v == "1" }
	groups := cfg.Usergroups[c.app.Key]
	out := []map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		g := groups[id]
		if g.Disabled && !truth(args["include_disabled"]) {
			continue
		}
		out = append(out, userGroupAnswer(c, id, g, truth(args["include_count"])))
	}
	writeOK(w, map[string]any{"usergroups": out})
}
