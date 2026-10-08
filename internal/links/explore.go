// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// The built-in "Explore" rule (C-12.FR-9): its name, and the Lookup table it reads, keyed by the environment label,
// else the cluster label, with the columns address and datasource_uid.
const (
	ExploreName  = "Explore"
	ExploreTable = "grafana"
)

// ExploreTemplate is the URL template of the built-in rule: the PromQL expression of the first firing Alert's
// generatorURL (its g0.expr parameter), opened in Grafana Explore at the address with the data source of the Lookup
// table "grafana", in Grafana's documented Explore URL format. Without the table, the row, a cell or an expression it
// writes nothing, which is no link and no error.
const ExploreTemplate = `{{- $key := or .Labels.environment .Labels.cluster -}}
{{- $address := lookup "grafana" $key "address" -}}
{{- $uid := lookup "grafana" $key "datasource_uid" -}}
{{- $expr := "" -}}
{{- range .Alerts.Firing -}}
{{- if and (not $expr) (regexMatch "[?&]g0\\.expr=[^&#]" .GeneratorURL) -}}
{{- $expr = regexReplaceAll "^[^?]*\\?(?:.*&)?g0\\.expr=([^&#]*).*$" "${1}" .GeneratorURL | urlUnescape -}}
{{- end -}}
{{- end -}}
{{- if and $address $uid $expr -}}
{{ trimSuffix "/" $address }}/explore?schemaVersion=1&orgId=1&panes=
{{- printf "{\"muster\":{\"datasource\":%s,\"queries\":[{\"refId\":\"A\",\"expr\":%s,\"datasource\":{\"type\":\"prometheus\",\"uid\":%s}}],\"range\":{\"from\":\"now-1h\",\"to\":\"now\"}}}" (toJson $uid) (toJson $expr) (toJson $uid) | urlquery }}
{{- end -}}`

// EnsureExplore creates the built-in "Explore" rule once per Organization, as a start-up ensure step: no Matchers, the
// scope alert_group and ExploreTemplate. It changes nothing when the rule exists, edited or not; a rule that is not
// the built-in one and already has its name stops the start with ErrRuleNameTaken.
func EnsureExplore(ctx context.Context, q Queries, orgID int64, now time.Time) error {
	_, err := q.EnsureExploreRule(ctx, dbgen.EnsureExploreRuleParams{OrgID: orgID,
		PublicID: publicid.New(publicid.LinkRule), Name: ExploreName, UrlTemplate: ExploreTemplate, Now: now.UTC()})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ensure the built-in link rule: %w", nameTaken(err, ruleNameKey, ErrRuleNameTaken))
	}
	return nil
}
