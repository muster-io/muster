// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

// The ids of the Route profiles (C-08.FR-7).
const (
	ProfileOnCall        = "on_call"
	ProfileInformational = "informational"
)

// Profile is a Route profile: a starting set of Route settings that only pre-fills a new Route and is never stored.
type Profile struct {
	ID       string
	Name     string
	Urgent   bool
	GroupKey []string
	Policy   Policy
}

// The values of the "Routing and lifecycle" table of defaults.md.
const (
	hour = 3600
	day  = 24 * hour
)

// onCall is the On-call profile, which the Default route also takes.
func onCall() Profile {
	return Profile{
		ID: ProfileOnCall, Name: "On-call", GroupKey: []string{"alertname", "severity", "cluster"},
		Policy: Policy{
			ReopenWindowSeconds: 15 * 60, GracePeriodSeconds: 15 * 60, UrgentRiseRemovesAck: true,
			SnoozeDurationsSeconds: []int64{hour, 4 * hour, day}, ThreadBatchingWindowSeconds: 60, StormThreshold: 20,
			Language:   LanguageEnglish,
			AckTimeout: AckTimeout{Enabled: true, FirstIntervalSeconds: 15 * 60},
			Reminders:  Reminders{Enabled: true, FirstIntervalSeconds: 4 * hour, CapSeconds: day},
		},
	}
}

// informational is the Informational profile: no ack timeout, no Reminders and long Snooze durations.
func informational() Profile {
	p := onCall()
	p.ID, p.Name = ProfileInformational, "Informational"
	p.Policy.SnoozeDurationsSeconds = []int64{day, 3 * day, 7 * day}
	p.Policy.AckTimeout.Enabled = false
	p.Policy.Reminders.Enabled = false
	return p
}

// Profiles are the Route profiles, On-call first, read from the built-in defaults; each call returns fresh copies.
func Profiles() []Profile {
	return []Profile{onCall(), informational()}
}
