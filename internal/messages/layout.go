// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"slices"
)

// MessageLayout is the content of a Root message as structured, plain-text data (C-29.FR-1, FR-8): the MessageLayout of
// the API and the `layout` of the Bridge protocol. It has the number and the title apart, the status by its key and
// word, the fields, the summary and the description, the key labels with their values — "2 values" where they differ
// — the Alerts by their distinguishing label with the total, the notices with the footer as their last line, the
// people and the links. The people are the Owner while the Alert Group is acknowledged, named without a Team (P-89),
// whose footer is then no notice but for "Escalation chains stopped", and before an acknowledgement the Assignees with
// the owning Team of their chain (C-32.FR-12), whose footer is no notice either; each names a person by their Muster
// display name, and the Bridge adapter adds the reference to their Mention entry.
type MessageLayout struct {
	Number      int64
	Title       string
	Status      string
	StatusWord  string
	Fields      []Label
	Summary     string
	Description string
	Labels      []Label
	Alerts      *LayoutAlerts
	Notices     []string
	Assignees   []LayoutPerson
	Owner       *LayoutPerson
	Links       []Link
}

// LayoutAlerts are the Alerts of a MessageLayout: the distinguishing label, the values listed and the total.
type LayoutAlerts struct {
	Label  string
	Values []AlertValue
	Total  int64
}

// LayoutPerson is a person of a MessageLayout by their Muster display name and, for an Assignee, the name of the owning
// Team of their chain, empty for none, both made safe.
type LayoutPerson struct {
	Name string
	Team string
}

// HasLayout reports whether the content of the Root message m is wholly Muster's compact default, which a MessageLayout
// carries (C-29.FR-8): not a Route's root_message template, whose Body renders items 3 to 5 (C-29.FR-5), nor its line
// template, whose output renders each listed Alert, not the Fallback template, which shows every label, and not a Desired state stored before the compact layout, without the
// status word. Those have content that only the body holds, so they have no layout.
func HasLayout(m Message) bool {
	return m.Kind == KindRoot && m.Heading != nil && m.StatusWord != "" && m.Body == nil && m.Alerts == nil &&
		(m.AlertsLine == nil || len(m.AlertsLine.Lines) == 0) && m.Environment == "" && len(m.GroupLabels) == 0 &&
		len(m.CommonLabels) == 0 && len(m.CommonAnnotations) == 0
}

// LayoutOf is the MessageLayout of the Root message m, its values as neutral text, from the fields the adapters lay
// out: a message shortened for a limit has the layout of what is left. The footer names its people by their Muster
// display names, also in a message whose footer an adapter chose (WithFooterIn); with an Owner, the footer of the
// acknowledgement is the Owner, and "Escalation chains stopped" a line of the notices when it stopped chains; with
// Assignees, the footer before an acknowledgement is the Assignees, who are never cut.
func LayoutOf(m Message) MessageLayout {
	out := MessageLayout{Status: m.Colour, StatusWord: m.StatusWord, Fields: slices.Clone(m.Fields),
		Summary: m.Summary, Description: m.Description, Notices: slices.Clone(m.Notices), Links: slices.Clone(m.Links)}
	if h := m.Heading; h != nil {
		out.Number, out.Title = h.Number, h.Title
	}
	for _, k := range m.KeyLabels {
		out.Labels = append(out.Labels, Label{Name: k.Name, Value: k.ValueText(m.Language)})
	}
	if a := m.AlertsLine; a != nil && a.Label != "" {
		out.Alerts = &LayoutAlerts{Label: a.Label, Values: slices.Clone(a.Values), Total: a.Total}
	}
	if m.Owner != "" && m.Colour == ColourAcknowledged {
		out.Owner = &LayoutPerson{Name: m.Owner}
		if m.ChainsStopped {
			out.Notices = append(out.Notices, T(m.Language, "footer.chainsStopped", nil))
		}
		return out
	}
	if len(m.Assignees) > 0 && m.Colour == ColourFiring {
		for _, a := range m.Assignees {
			out.Assignees = append(out.Assignees, LayoutPerson(a))
		}
		return out
	}
	if footer := m.WithFooterIn("", nil).FooterNamed(); footer != "" {
		out.Notices = append(out.Notices, footer)
	}
	return out
}
