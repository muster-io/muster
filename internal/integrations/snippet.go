// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package integrations

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RepeatInterval is snippet.repeat_interval: the repeat_interval of the snippet's route, inside the recommended range
// of 5 to 15 minutes.
const RepeatInterval = 10 * time.Minute

// plainScalar is a YAML scalar that needs no quotes: it cannot be read as another type or as YAML syntax.
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./:@%+~=?&-]*$`)

// Snippet is the Alertmanager configuration of an Integration token (C-05.FR-5): an Alertmanager receiver
// muster-<name> whose webhook sends every alert of an Alertmanager group (max_alerts 0) and its resolves to the
// ingestion URL with the token as a bearer token, and a child route for it with continue, to be placed first, with
// snippet.repeat_interval and a comment on its range. Because a first child route without matchers matches every
// alert, Alertmanager no longer falls back to the top-level receiver: the snippet ends with a commented catch-all child
// route for that receiver, to be kept last.
func Snippet(integrationName, ingestURL, token string) string {
	receiver := scalar("muster-" + integrationName)
	var b strings.Builder
	b.WriteString("# Alertmanager receiver for Muster, Integration " + scalar(integrationName) +
		": add it to the receivers.\n")
	b.WriteString("receivers:\n")
	b.WriteString("  - name: " + receiver + "\n")
	b.WriteString("    webhook_configs:\n")
	b.WriteString("      - url: " + scalar(ingestURL) + "\n")
	b.WriteString("        send_resolved: true\n")
	b.WriteString("        max_alerts: 0\n")
	b.WriteString("        http_config:\n")
	b.WriteString("          authorization:\n")
	b.WriteString("            type: Bearer\n")
	b.WriteString("            credentials: " + scalar(token) + "\n")
	b.WriteString("\n")
	b.WriteString("# Place this child route first under the top-level route. Without matchers it takes every alert, and\n")
	b.WriteString("# continue: true passes each alert on to the child routes after it.\n")
	b.WriteString("# Alertmanager uses the receiver of the top-level route only for an alert that no child route matches, and\n")
	b.WriteString("# this route matches every alert: keep a catch-all child route for your default receiver last, or make sure\n")
	b.WriteString("# a later child route catches everything, or your default receiver stops getting alerts.\n")
	b.WriteString("route:\n")
	b.WriteString("  routes:\n")
	b.WriteString("    - receiver: " + receiver + "\n")
	b.WriteString("      continue: true\n")
	b.WriteString("      # Keep repeat_interval between 5 and 15 minutes: Muster learns from the repeats that alerts still fire\n")
	b.WriteString("      # and that missing ones are gone. Noise is controlled in Muster, not by long repeat intervals.\n")
	b.WriteString("      repeat_interval: " + duration(RepeatInterval) + "\n")
	b.WriteString("    # ... your existing child routes\n")
	b.WriteString("    # The last child route: a catch-all without matchers for the receiver of the top-level route.\n")
	b.WriteString("    # - receiver: <your default receiver>\n")
	return b.String()
}

// scalar writes s as a YAML scalar: plain when that is safe, otherwise double-quoted, which JSON quoting is.
func scalar(s string) string {
	if plainScalar.MatchString(s) && !reserved(s) {
		return s
	}
	b, _ := json.Marshal(s) // a string always encodes
	return string(b)
}

// reserved reports whether a plain s would be read as something other than a string.
func reserved(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", "y", "n":
		return true
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// duration writes d in the duration syntax of Alertmanager, such as 10m.
func duration(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	if d%time.Minute == 0 {
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}
