-- Bad: each "want: 1" marks a line where rule 1 must report.

-- name: ListAllAlerts :many
SELECT id FROM alerts; -- want: 1

-- name: ListAlertsOfGroup :many
SELECT id FROM alerts WHERE group_id = $1; -- want: 1

-- name: ListAlertsOr :many
SELECT id FROM alerts WHERE org_id = $1 OR id = $2; -- want: 1

-- name: ListAlertsNotIn :many
SELECT id FROM alerts WHERE org_id NOT IN ($1); -- want: 1

-- name: ListAlertsSelfCompare :many
SELECT id FROM alerts WHERE org_id = org_id; -- want: 1

-- name: ListAlertsComparedWithOwnColumn :many
SELECT id FROM alerts WHERE org_id = group_id; -- want: 1

-- name: ListAlertsJoinUnfiltered :many
SELECT a.id
FROM alerts a
JOIN alert_groups g ON g.id = a.group_id -- want: 1
WHERE a.org_id = @org_id;

-- name: ListAlertsJoinedOnOrgOnly :many
SELECT a.id
FROM alerts a -- want: 1
JOIN alert_groups g ON g.org_id = a.org_id AND g.id = a.group_id; -- want: 1

-- name: ListAlertsAmbiguous :many
SELECT a.id
FROM alerts a -- want: 1
JOIN alert_groups g ON g.id = a.group_id -- want: 1
WHERE org_id = @org_id;

-- name: ListAlertsInAnyGroup :many
SELECT id FROM alerts
WHERE org_id = @org_id AND group_id IN (SELECT id FROM alert_groups); -- want: 1

-- name: ListAlertsViaUnfilteredCTE :many
WITH g AS (SELECT id FROM alert_groups) -- want: 1
SELECT id FROM alerts WHERE org_id = @org_id AND group_id IN (SELECT id FROM g);

-- name: ListAlertsFromUnfilteredSubquery :many
SELECT s.id FROM (SELECT id FROM alerts) AS s; -- want: 1

-- name: ListAlertsAndAllIntegrations :many
SELECT id FROM alerts WHERE org_id = $1
UNION ALL
SELECT id FROM integrations; -- want: 1

-- name: ListSchemaQualified :many
SELECT id FROM public.alerts; -- want: 1

-- name: ListPartition :many
SELECT body FROM events_2026; -- want: 1

-- name: ListAttachedPartition :many
SELECT body FROM events_2027; -- want: 1

-- name: ListDestinations :many
SELECT id FROM destinations; -- want: 1

-- name: ListArchived :many
SELECT id FROM archived_alerts; -- want: 1

-- name: ListRenamed :many
SELECT id FROM drafts_v2; -- want: 1

-- name: SetAlertStateEverywhere :exec
UPDATE alerts SET state = $2 WHERE id = $1; -- want: 1

-- name: ResolveAlertsFromUnfilteredIntegration :exec
UPDATE alerts a SET state = 'resolved'
FROM integrations i -- want: 1
WHERE a.integration_id = i.id AND a.org_id = @org_id;

-- name: DeleteAlert :exec
DELETE FROM alerts WHERE id = $1; -- want: 1

-- name: CreateAlertWithoutOrg :one
INSERT INTO alerts (group_id, integration_id, state) VALUES ($1, $2, 'firing') RETURNING id; -- want: 1

-- name: CreateAlertWithoutColumns :exec
INSERT INTO alerts VALUES ($1, $2, $3, $4, 'firing'); -- want: 1

-- name: CopyAllAlerts :exec
INSERT INTO archived_alerts (id, org_id, group_id, integration_id, state)
SELECT id, org_id, group_id, integration_id, state FROM alerts; -- want: 1

-- name: GetIntegrationWithEmptyExemption :one
-- archlint:org-exempt
SELECT id -- want: 1
FROM integrations WHERE token_hash = $1; -- want: 1

SELECT id FROM alerts WHERE state = 'firing'; -- want: 1

-- name: ListAlertsOfEveryOrganization :many
SELECT id FROM alerts WHERE org_id IN (SELECT id FROM organizations); -- want: 1

-- name: ListAlertsOfFirstOrganization :many
SELECT id FROM alerts WHERE org_id = (SELECT id FROM organizations LIMIT 1); -- want: 1

-- name: ListAlertsJoinedToOrganizations :many
WITH o AS (SELECT id AS org_id FROM organizations)
SELECT a.id FROM alerts a JOIN o ON a.org_id = o.org_id; -- want: 1

-- name: ListAlertsOfConstantOrganization :many
SELECT id FROM alerts WHERE org_id = 1; -- want: 1

-- name: ListAlertsIsDistinct :many
SELECT id FROM alerts WHERE org_id IS DISTINCT FROM $1; -- want: 1

-- name: ListAlertsFilteredOnlyInLeftJoin :many
SELECT a.id
FROM alerts a -- want: 1
LEFT JOIN alert_groups g ON g.id = a.group_id AND g.org_id = $1 AND a.org_id = $1;

-- name: ListAlertsLinkedFromLeftJoinedSide :many
SELECT a.id
FROM alerts a -- want: 1
LEFT JOIN alert_groups g ON g.id = a.group_id AND g.org_id = $1 AND a.org_id = g.org_id;

-- name: ListAlertsFilteredOnlyInRightJoin :many
SELECT a.id
FROM alert_groups g
RIGHT JOIN alerts a ON g.id = a.group_id AND g.org_id = $1 AND a.org_id = $1; -- want: 1

-- name: ListAlertsFilteredOnlyInFullJoin :many
SELECT a.id
FROM alerts a -- want: 1
FULL JOIN alert_groups g ON g.id = a.group_id AND g.org_id = $1 AND a.org_id = $1; -- want: 1

-- name: ListGroupChainOfEveryOrganization :many
WITH RECURSIVE chain AS (
    SELECT id, org_id, parent_id FROM alert_groups WHERE id = @id -- want: 1
    UNION ALL
    SELECT g.id, g.org_id, g.parent_id FROM alert_groups g JOIN chain c ON g.id = c.parent_id AND g.org_id = c.org_id -- want: 1
)
SELECT id FROM chain;

-- name: ListOpenAlertsOfEveryOrganization :many
SELECT id FROM open_alerts; -- want: 1

-- name: ListEveryAlert :many
SELECT id FROM every_alert; -- want: 1

-- name: ListAlertsByOrg :many
SELECT alert_id FROM alerts_by_org; -- want: 1

-- name: TruncateAlerts :exec
TRUNCATE alerts; -- want: 1

-- name: GetIntegrationExemptedOnItsLine :one
SELECT id FROM integrations WHERE token_hash = $1; -- archlint:org-exempt by-hash lookup // want: 1

-- name: ListAlertsAfterAnExemptLine :many
SELECT id FROM alerts; -- want: 1

-- name: ClaimDeliveriesOfEveryOrganization :many
UPDATE deliveries SET state = 'sending', lease_until = @lease_until -- want: 1
WHERE id IN (
    SELECT id FROM deliveries
    WHERE org_id = @org_id AND state = 'pending'
    ORDER BY id
    LIMIT @n
    FOR UPDATE SKIP LOCKED
)
RETURNING id;
