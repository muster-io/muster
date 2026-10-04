-- Good: the lookups by hash that design/db/schema.md names carry an exemption with a reason.

-- name: GetIntegrationByTokenHash :one
-- archlint:org-exempt by-hash lookup: the token is all the request carries
SELECT id, org_id FROM integrations WHERE token_hash = $1;

-- name: ResetIntegrations :exec
-- archlint:org-exempt the exemption covers TRUNCATE as any other statement
TRUNCATE integrations;
