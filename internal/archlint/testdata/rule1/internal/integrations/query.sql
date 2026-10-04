-- Good: the lookups by hash that design/db/schema.md names carry an exemption with a reason.

-- name: GetIntegrationByTokenHash :one
-- archlint:org-exempt by-hash lookup: the token is all the request carries
SELECT id, org_id FROM integrations WHERE token_hash = $1;
