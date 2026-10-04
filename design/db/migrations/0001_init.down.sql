-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0001_init.up.sql. Dropping a partitioned table drops every partition Muster created at runtime.
-- Downgrades are not supported in production (back up before upgrading); this file exists for the
-- `up -> down -> up` acceptance check and for development.
--
-- The pg_trgm extension is left in place on purpose: it may have been created by a database administrator and
-- may be used by other schemas; `CREATE EXTENSION IF NOT EXISTS` in the up migration accepts it.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP VIEW encrypted_values;

DROP TABLE audit_log;
DROP FUNCTION audit_log_reject_change();

DROP TABLE link_rule_matchers;
DROP TABLE link_rules;
DROP TABLE lookup_table_entries;
DROP TABLE lookup_tables;

DROP TABLE rate_limit_buckets;
DROP TABLE telegram_post_copies;
DROP TABLE delivery_events;
DROP TABLE webhook_events;
DROP TABLE thread_replies;
DROP TABLE deliveries;
DROP TABLE timers;
DROP TABLE storms;

DROP TABLE notes;
DROP TABLE timeline_entries;
DROP TABLE alert_group_alerts;
DROP TABLE alert_groups;

DROP TABLE alert_presences;
DROP TABLE alerts;

DROP TABLE route_suggestion_dismissals;
DROP TABLE route_destinations;
DROP TABLE route_matchers;
DROP TABLE routes;

DROP TABLE alertmanager_groups;
DROP TABLE alertmanager_routes;

DROP TABLE snapshot_bodies;
DROP TABLE stored_snapshots;
DROP TABLE ingest_claims;
DROP TABLE integration_tokens;
DROP TABLE integrations;

DROP TABLE account_link_requests;
DROP TABLE account_links;
DROP TABLE destination_secrets;
DROP TABLE destinations;
DROP TABLE connections;

DROP TABLE api_token_permissions;
DROP TABLE api_tokens;
DROP TABLE service_accounts;

DROP TABLE oidc_settings;
DROP TABLE oidc_auth_requests;
DROP TABLE password_setups;
DROP TABLE sign_in_throttles;
DROP TABLE oidc_checks;
DROP TABLE sessions;
DROP TABLE user_recovery_codes;
DROP TABLE user_totp;
DROP TABLE users;

DROP TABLE alert_group_counters;
DROP TABLE outbound_policies;
DROP TABLE organizations;

DROP TABLE downtime_periods;
DROP TABLE runtime_state;
DROP TABLE replicas;
DROP TABLE keyring_state;

DROP TABLE role_permissions;
DROP TABLE permissions;
DROP TABLE roles;

COMMIT;
