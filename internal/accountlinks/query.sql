-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Account links (C-18): the messenger accounts linked to Users, one per identity space and User. S-061 reads them
-- only, to run a button press as the linked User; S-051 adds linking and unlinking.

-- LookupAccountLink reads the User that the messenger account external_id of the identity space is linked to; a
-- deleted User has no links (C-18.FR-8).
-- name: LookupAccountLink :one
SELECT u.id, u.public_id, u.login, u.name, u.role, u.status
FROM account_links al
JOIN users u ON u.org_id = al.org_id AND u.id = al.user_id
WHERE al.org_id = @org_id AND al.identity_space = @identity_space AND al.external_id = @external_id
  AND u.status <> 'deleted';
