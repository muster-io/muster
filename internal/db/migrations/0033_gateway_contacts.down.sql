-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0033_gateway_contacts.up.sql. Every contact at a Notification gateway and every confirmation code is lost;
-- the Audit log entries about the contacts stay.

BEGIN;

SET LOCAL lock_timeout = '5s';

DROP TABLE gateway_contact_codes;

DROP TABLE gateway_contacts;

COMMIT;
