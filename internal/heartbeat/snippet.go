// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package heartbeat

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// RepeatInterval is snippet.heartbeat_repeat_interval: the repeat_interval of the Heartbeat route, so that
// Alertmanager sends the always-firing alert about once a minute.
const RepeatInterval = time.Minute

// AlertName is the alertname of the always-firing rule of the snippet.
const AlertName = "MusterHeartbeat"

// quote writes s as a double-quoted YAML scalar, which JSON quoting is.
func quote(s string) string {
	b, _ := json.Marshal(s) // a string always encodes
	return string(b)
}

// Snippet is the Heartbeat configuration of an Integration token (C-07.FR-6): a Prometheus rule MusterHeartbeat that
// always fires, with the note that kube-prometheus-stack's Watchdog serves instead; an Alertmanager child route for
// it with continue, group_wait 0s, group_interval 1m and snippet.heartbeat_repeat_interval; and a receiver whose
// webhook sends it, without resolves, to the Heartbeat URL with the token as a bearer token.
func Snippet(integrationName, heartbeatURL, token string) string {
	receiver := quote("muster-heartbeat-" + integrationName)
	var b strings.Builder
	b.WriteString("# Heartbeat for Muster, Integration " + quote(integrationName) + ".\n")
	b.WriteString("# 1. A Prometheus rule that always fires. kube-prometheus-stack already has one, Watchdog: use it\n")
	b.WriteString("#    instead of this rule and match alertname=\"Watchdog\" in the route below.\n")
	b.WriteString("groups:\n")
	b.WriteString("  - name: muster-heartbeat\n")
	b.WriteString("    rules:\n")
	b.WriteString("      - alert: " + AlertName + "\n")
	b.WriteString("        expr: vector(1)\n")
	b.WriteString("        annotations:\n")
	b.WriteString("          summary: Always fires, so that Muster hears from Alertmanager every minute.\n")
	b.WriteString("\n")
	b.WriteString("# 2. An Alertmanager child route for it: place it first under the top-level route. continue: true\n")
	b.WriteString("#    passes the alert on to your other routes, such as a dead man's switch.\n")
	b.WriteString("route:\n")
	b.WriteString("  routes:\n")
	b.WriteString("    - receiver: " + receiver + "\n")
	b.WriteString("      matchers: ['alertname=\"" + AlertName + "\"']\n")
	b.WriteString("      continue: true\n")
	b.WriteString("      group_wait: 0s\n")
	b.WriteString("      group_interval: 1m\n")
	b.WriteString("      repeat_interval: " + minutes(RepeatInterval) + "\n")
	b.WriteString("\n")
	b.WriteString("# 3. The Alertmanager receiver that sends it to Muster's Heartbeat URL: add it to the receivers.\n")
	b.WriteString("receivers:\n")
	b.WriteString("  - name: " + receiver + "\n")
	b.WriteString("    webhook_configs:\n")
	b.WriteString("      - url: " + heartbeatURL + "\n")
	b.WriteString("        send_resolved: false\n")
	b.WriteString("        http_config:\n")
	b.WriteString("          authorization:\n")
	b.WriteString("            type: Bearer\n")
	b.WriteString("            credentials: " + token + "\n")
	return b.String()
}

// minutes writes a whole number of minutes in the duration syntax of Alertmanager, such as 1m.
func minutes(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}
