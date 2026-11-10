-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0037_notification_rules.up.sql. Every User's own Notification rules are lost, and so are the Organization's
-- default rules; once 0037 is applied again, every User follows the Organization's rules, which are the defaults. The
-- Audit log entries about the rules stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP TABLE notification_rules;

ALTER TABLE organizations
    DROP CONSTRAINT organizations_notification_rules_check,
    DROP COLUMN notification_rules;

COMMIT;
