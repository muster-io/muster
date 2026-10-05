// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

const day = 24 * time.Hour

// Defaults returns the settings of a new Organization, the defaults of defaults.md; ID and PublicID are empty.
func Defaults() Settings {
	return Settings{
		Name:          "Muster",
		TimeZone:      "UTC",
		SeverityLabel: "severity",
		// Any other value counts as warning, and a missing label as info (organization.severity_mapping).
		SeverityMapping: []SeverityMapping{
			{Value: "critical", Level: SeverityCritical},
			{Value: "warning", Level: SeverityWarning},
			{Value: "info", Level: SeverityInfo},
			{Value: "none", Level: SeverityInfo},
		},
		SeverityStyles: []SeverityStyle{
			{Level: SeverityCritical, Emoji: "🟥", Color: "#d32f2f"},
			{Level: SeverityWarning, Emoji: "🟧", Color: "#f57c00"},
			{Level: SeverityInfo, Emoji: "🟦", Color: "#1976d2"},
		},
		CriticalIsUrgent: true,
		InstanceLabels:   []string{"pod", "instance", "container", "endpoint"},
		Retention: Retention{
			StoredSnapshotsDays:     14,
			AlertDetailsDays:        90,
			AlertGroupSummariesDays: 730,
			AuditLogDays:            365,
		},
		TOTPRequired:   TOTPNobody,
		OIDCTokenGrace: 7 * day,
		Outbound:       OutboundPolicy{Mode: OutboundStandard, Allowed: []string{}, Denied: []string{}},
	}
}

// Ensure is the start-up step of the Organization: it creates the Organization with Defaults when there is none, and
// its outbound address policy when that is missing. It changes nothing that exists, and does not create the Alert
// Group counter row, which only internal/groups writes. now is the business time.
func Ensure(ctx context.Context, s Store, log *logging.Logger, now time.Time) error {
	orgID, created, err := ensureOrganization(ctx, s, now)
	if err != nil {
		return err
	}
	d := Defaults().Outbound
	if _, err := s.CreateOutboundPolicy(ctx, dbgen.CreateOutboundPolicyParams{
		OrgID: orgID, Policy: string(d.Mode), Allowed: d.Allowed, Denied: d.Denied, UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("create the outbound address policy: %w", err)
	}
	if created != "" {
		log.Log(ctx, logging.OrganizationCreated, logging.F("organization", created))
	}
	return nil
}

// ensureOrganization returns the Organization's id and, when it created it, its public_id.
func ensureOrganization(ctx context.Context, s Store, now time.Time) (int64, string, error) {
	org, err := s.GetOrganization(ctx)
	if err == nil {
		return org.ID, "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Errorf("read the organization: %w", err)
	}
	d := Defaults()
	mapping, err := json.Marshal(d.SeverityMapping)
	if err != nil {
		return 0, "", err
	}
	styles, err := json.Marshal(d.SeverityStyles)
	if err != nil {
		return 0, "", err
	}
	id := publicid.New(publicid.Organization)
	orgID, err := s.CreateOrganization(ctx, dbgen.CreateOrganizationParams{
		PublicID:                         id,
		Name:                             d.Name,
		TimeZone:                         d.TimeZone,
		SeverityLabel:                    d.SeverityLabel,
		SeverityMapping:                  mapping,
		SeverityStyles:                   styles,
		CriticalIsUrgent:                 d.CriticalIsUrgent,
		InstanceLabels:                   d.InstanceLabels,
		RetentionStoredSnapshotsDays:     d.Retention.StoredSnapshotsDays,
		RetentionAlertDetailsDays:        d.Retention.AlertDetailsDays,
		RetentionAlertGroupSummariesDays: d.Retention.AlertGroupSummariesDays,
		RetentionAuditLogDays:            d.Retention.AuditLogDays,
		TotpRequired:                     string(d.TOTPRequired),
		OidcTokenGraceSeconds:            int64(d.OIDCTokenGrace / time.Second),
		CreatedAt:                        now,
	})
	if err != nil {
		return 0, "", fmt.Errorf("create the organization: %w", err)
	}
	return orgID, id, nil
}
