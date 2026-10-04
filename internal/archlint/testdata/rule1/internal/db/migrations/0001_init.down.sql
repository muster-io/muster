-- Down migrations are not read: if they were, roles would count as org-scoped.
ALTER TABLE roles ADD COLUMN org_id bigint;
