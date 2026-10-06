// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/organization"
)

// GetOrganization is getOrganization: the organization resource for every signed-in identity, its Secrets shown only
// as set or not (C-03.FR-20, FR-21).
func (s *Server) GetOrganization(ctx context.Context, _ gen.GetOrganizationRequestObject) (
	gen.GetOrganizationResponseObject, error) {
	o, err := s.organization.Get(ctx)
	if err != nil {
		return nil, err
	}
	tag := etag(o.Version)
	return gen.GetOrganization200JSONResponse{Body: organizationOf(o),
		Headers: gen.GetOrganization200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateOrganization is updateOrganization with If-Match: in this release it changes the TOTP policy, and a changed
// value of any other field is 422 with unsupported at that field (C-03.FR-20).
func (s *Server) UpdateOrganization(ctx context.Context, req gen.UpdateOrganizationRequestObject) (
	gen.UpdateOrganizationResponseObject, error) {
	r, err := requester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, fieldProblem(http.StatusBadRequest, "", fieldRequired, "The request body is missing.")
	}
	o, err := s.organization.Update(ctx, r.Actor, r.Transport, r.Address, version, inputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(o.Version)
	return gen.UpdateOrganization200JSONResponse{Body: organizationOf(o),
		Headers: gen.UpdateOrganization200ResponseHeaders{ETag: &tag}}, nil
}

// organizationOf is the API form of the organization resource.
func organizationOf(o organization.Organization) gen.Organization {
	tag := etag(o.Version)
	out := gen.Organization{
		Id: o.PublicID, Name: o.Name, TimeZone: o.TimeZone, SeverityLabel: o.SeverityLabel,
		CriticalIsUrgent: o.CriticalIsUrgent, InstanceLabels: o.InstanceLabels,
		SeverityMapping: make([]gen.SeverityMappingEntry, 0, len(o.SeverityMapping)),
		SeverityStyles:  make([]gen.SeverityLevelStyle, 0, len(o.SeverityStyles)),
		Retention: gen.RetentionSettings{
			StoredSnapshotsDays: int(o.Retention.StoredSnapshotsDays), AlertDetailsDays: int(o.Retention.AlertDetailsDays),
			AlertGroupSummariesDays: int(o.Retention.AlertGroupSummariesDays),
			AuditLogDays:            int(o.Retention.AuditLogDays),
		},
		TotpRequired: gen.TotpPolicy(o.TOTPRequired), OidcTokenGraceSeconds: int(o.OIDCTokenGrace / time.Second),
		Etag: &tag,
	}
	for _, m := range o.SeverityMapping {
		out.SeverityMapping = append(out.SeverityMapping, gen.SeverityMappingEntry{Value: m.Value,
			Level: gen.SeverityLevel(m.Level)})
	}
	for _, st := range o.SeverityStyles {
		out.SeverityStyles = append(out.SeverityStyles, gen.SeverityLevelStyle{Level: gen.SeverityLevel(st.Level),
			Emoji: st.Emoji, Color: st.Color})
	}
	hb := o.OutgoingHeartbeat
	out.OutgoingHeartbeat.UrlStatus = secretStatusOf(hb.URL)
	password := secretStatusOf(hb.ProxyPassword)
	out.OutgoingHeartbeat.Proxy = gen.ProxyConfig{Enabled: hb.Proxy.Enabled, PasswordStatus: &password}
	if hb.Proxy.Type != "" {
		t := gen.ProxyType(hb.Proxy.Type)
		out.OutgoingHeartbeat.Proxy.Type = &t
	}
	if hb.Proxy.Address != "" {
		addr := hb.Proxy.Address
		out.OutgoingHeartbeat.Proxy.Address = &addr
	}
	if hb.Proxy.Username != nil {
		out.OutgoingHeartbeat.Proxy.Username.Set(*hb.Proxy.Username)
	}
	return out
}

func secretStatusOf(st organization.SecretState) gen.SecretStatus {
	out := gen.SecretStatus{Set: st.Set}
	if st.UpdatedAt != nil {
		out.UpdatedAt.Set(st.UpdatedAt.UTC())
	} else {
		out.UpdatedAt.SetNull()
	}
	return out
}

// inputOf is an update of the organization resource as internal/organization takes it.
func inputOf(b gen.UpdateOrganizationJSONRequestBody) organization.Input {
	in := organization.Input{
		Name: b.Name, TimeZone: b.TimeZone, SeverityLabel: b.SeverityLabel, CriticalIsUrgent: b.CriticalIsUrgent,
		InstanceLabels: b.InstanceLabels, TOTPRequired: organization.TOTPPolicy(b.TotpRequired),
		OIDCTokenGraceSeconds: int64(b.OidcTokenGraceSeconds),
		Retention: organization.Retention{
			StoredSnapshotsDays: int64(b.Retention.StoredSnapshotsDays), AlertDetailsDays: int64(b.Retention.AlertDetailsDays),
			AlertGroupSummariesDays: int64(b.Retention.AlertGroupSummariesDays),
			AuditLogDays:            int64(b.Retention.AuditLogDays),
		},
		HeartbeatURL: secretInputOf(b.OutgoingHeartbeat.Url),
	}
	if in.InstanceLabels == nil {
		in.InstanceLabels = []string{}
	}
	for _, m := range b.SeverityMapping {
		in.SeverityMapping = append(in.SeverityMapping, organization.SeverityMapping{Value: m.Value,
			Level: organization.SeverityLevel(m.Level)})
	}
	for _, st := range b.SeverityStyles {
		in.SeverityStyles = append(in.SeverityStyles, organization.SeverityStyle{
			Level: organization.SeverityLevel(st.Level), Emoji: st.Emoji, Color: st.Color})
	}
	p := b.OutgoingHeartbeat.Proxy
	in.HeartbeatProxy = organization.ProxyInput{Enabled: p.Enabled, Address: p.Address,
		UsernameSet: p.Username.IsSpecified(), Password: secretInputOf(p.Password)}
	if p.Type != nil {
		t := string(*p.Type)
		in.HeartbeatProxy.Type = &t
	}
	if p.Username.IsSpecified() && !p.Username.IsNull() {
		u := p.Username.MustGet()
		in.HeartbeatProxy.Username = &u
	}
	return in
}

func secretInputOf(v nullable.Nullable[string]) organization.SecretInput {
	in := organization.SecretInput{Given: v.IsSpecified(), Null: v.IsNull()}
	if in.Given && !in.Null {
		in.Value = v.MustGet()
	}
	return in
}
