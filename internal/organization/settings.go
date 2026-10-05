// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package organization holds the single Organization of L1 and its settings (C-02.FR-23): the start-up step that
// creates it with the defaults of defaults.md together with its outbound address policy, and the read accessor that
// later capabilities use. Editing the settings arrives with C-03, C-19 and C-20.
package organization

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/organization/dbgen"
)

// SeverityLevel is one of the three Severity levels.
type SeverityLevel string

const (
	SeverityCritical SeverityLevel = "critical"
	SeverityWarning  SeverityLevel = "warning"
	SeverityInfo     SeverityLevel = "info"
)

// SeverityMapping maps a value of the severity label to a Severity level.
type SeverityMapping struct {
	Value string        `json:"value"`
	Level SeverityLevel `json:"level"`
}

// SeverityStyle is the emoji and colour of a Severity level in messages.
type SeverityStyle struct {
	Level SeverityLevel `json:"level"`
	Emoji string        `json:"emoji"`
	Color string        `json:"color"`
}

// Retention are the retention periods, in days.
type Retention struct {
	StoredSnapshotsDays     int64
	AlertDetailsDays        int64
	AlertGroupSummariesDays int64
	AuditLogDays            int64
}

// TOTPPolicy says who must use a second factor.
type TOTPPolicy string

const (
	TOTPNobody     TOTPPolicy = "nobody"
	TOTPLocalUsers TOTPPolicy = "local_users"
	TOTPEveryone   TOTPPolicy = "everyone"
)

// OutboundMode is the outbound address policy: standard or strict.
type OutboundMode string

const (
	OutboundStandard OutboundMode = "standard"
	OutboundStrict   OutboundMode = "strict"
)

// OutboundPolicy is the outbound address policy with its allowed and denied networks, host names and domains.
type OutboundPolicy struct {
	Mode    OutboundMode
	Allowed []string
	Denied  []string
}

// Settings are the Organization and its settings.
type Settings struct {
	ID               int64
	PublicID         string
	Name             string
	TimeZone         string
	SeverityLabel    string
	SeverityMapping  []SeverityMapping
	SeverityStyles   []SeverityStyle
	CriticalIsUrgent bool
	InstanceLabels   []string
	Retention        Retention
	TOTPRequired     TOTPPolicy
	// OIDCTokenGrace is auth.oidc_token_grace.
	OIDCTokenGrace time.Duration
	Outbound       OutboundPolicy
}

// Store is the organization's part of the database. *dbgen.Queries implements it.
type Store interface {
	GetOrganization(ctx context.Context) (dbgen.GetOrganizationRow, error)
	CreateOrganization(ctx context.Context, arg dbgen.CreateOrganizationParams) (int64, error)
	CreateOutboundPolicy(ctx context.Context, arg dbgen.CreateOutboundPolicyParams) (int64, error)
	GetOutboundPolicy(ctx context.Context, orgID int64) (dbgen.GetOutboundPolicyRow, error)
}

// NewStore is the Store over a pool, a connection or a transaction.
func NewStore(db dbgen.DBTX) Store {
	return dbgen.New(db)
}

// Current reads the Organization and its settings.
func Current(ctx context.Context, s Store) (Settings, error) {
	org, err := s.GetOrganization(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("read the organization: %w", err)
	}
	policy, err := s.GetOutboundPolicy(ctx, org.ID)
	if err != nil {
		return Settings{}, fmt.Errorf("read the outbound address policy: %w", err)
	}
	st := Settings{
		ID:               org.ID,
		PublicID:         org.PublicID,
		Name:             org.Name,
		TimeZone:         org.TimeZone,
		SeverityLabel:    org.SeverityLabel,
		CriticalIsUrgent: org.CriticalIsUrgent,
		InstanceLabels:   org.InstanceLabels,
		Retention: Retention{
			StoredSnapshotsDays:     org.RetentionStoredSnapshotsDays,
			AlertDetailsDays:        org.RetentionAlertDetailsDays,
			AlertGroupSummariesDays: org.RetentionAlertGroupSummariesDays,
			AuditLogDays:            org.RetentionAuditLogDays,
		},
		TOTPRequired:   TOTPPolicy(org.TotpRequired),
		OIDCTokenGrace: time.Duration(org.OidcTokenGraceSeconds) * time.Second,
		Outbound:       OutboundPolicy{Mode: OutboundMode(policy.Policy), Allowed: policy.Allowed, Denied: policy.Denied},
	}
	if err := json.Unmarshal(org.SeverityMapping, &st.SeverityMapping); err != nil {
		return Settings{}, fmt.Errorf("read the severity mapping: %w", err)
	}
	if err := json.Unmarshal(org.SeverityStyles, &st.SeverityStyles); err != nil {
		return Settings{}, fmt.Errorf("read the severity styles: %w", err)
	}
	return st, nil
}
