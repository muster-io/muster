// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package managedby

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// markQueries are the named queries, by package, that read or write managed_by: the reads, creations and updates of
// the twelve kinds of C-33.FR-11, the writes of the mark alone of Connections and Destinations, and the lock of a
// Connection, which reads managed_by_version for its ETag. Every other query leaves the column alone, so that a write
// that does not take the field — a rename, a Secret, a check, a health or a start-up step — keeps the mark, which the
// domain tests, whose stores are fakes, cannot see.
var markQueries = map[string][]string{
	"connections": {"ListConnections", "GetConnection", "LockConnection", "InsertConnection", "UpdateConnection",
		"SetConnectionMark"},
	"destinations": {"ListDestinations", "GetDestination", "InsertMattermostDestination",
		"UpdateMattermostDestination", "InsertTelegramDestination", "UpdateTelegramDestination",
		"InsertBridgeDestination", "UpdateBridgeDestination", "InsertSlackDestination", "UpdateSlackDestination",
		"InsertWebhookDestination", "UpdateWebhookDestination", "SetDestinationMark"},
	"escalation":   {"ListChains", "GetChain", "InsertChain", "UpdateChain"},
	"integrations": {"ListIntegrations", "GetIntegration", "InsertIntegration", "UpdateIntegration"},
	"links": {"ListLookupTables", "GetLookupTable", "InsertLookupTable", "UpdateLookupTable", "ListLinkRules",
		"GetLinkRule", "InsertLinkRule", "UpdateLinkRule"},
	"oidc":         {"GetSettings", "InsertSettings", "UpdateSettings"},
	"organization": {"GetOutboundPolicySettings", "UpdateOutboundPolicy", "GetSettings", "UpdateSettings"},
	"routing":      {"ListRoutes", "GetRoute", "InsertRoute", "UpdateRoute"},
	"schedules":    {"ListSchedules", "GetSchedule", "InsertSchedule", "UpdateSchedule"},
	"teams":        {"ListTeams", "GetTeam", "InsertTeam", "UpdateTeam"},
}

// queryName is the line that names a query of sqlc.
var queryName = regexp.MustCompile(`^-- name: (\w+)\s`)

// TestQueriesOfTheMark: exactly the queries of markQueries mention managed_by in their SQL, outside comments, in the
// query.sql of every domain package, so that a query added or changed to read or write the column is a decision.
func TestQueriesOfTheMark(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "query.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query.sql beside internal/managedby: %v", err)
	}
	found := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pkg, name := filepath.Base(filepath.Dir(f)), ""
		for line := range strings.SplitSeq(string(b), "\n") {
			if m := queryName.FindStringSubmatch(line); m != nil {
				name = m[1]
				continue
			}
			code, _, _ := strings.Cut(line, "--")
			if name != "" && strings.Contains(code, "managed_by") && !slices.Contains(found[pkg], name) {
				found[pkg] = append(found[pkg], name)
			}
		}
	}
	for _, pkg := range slices.Sorted(maps.Keys(markQueries)) {
		if _, ok := found[pkg]; !ok {
			found[pkg] = nil
		}
	}
	for pkg, names := range found {
		for _, name := range names {
			if !slices.Contains(markQueries[pkg], name) {
				t.Errorf("internal/%s/query.sql: %s mentions managed_by and is not one of markQueries", pkg, name)
			}
		}
		for _, name := range markQueries[pkg] {
			if !slices.Contains(names, name) {
				t.Errorf("internal/%s/query.sql: %s of markQueries does not mention managed_by", pkg, name)
			}
		}
	}
}
