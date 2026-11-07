-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Reverts 0035_gateway_call_signature.up.sql. The calls lose their signed action data and the id of the key that
-- signed it: a call being retried is signed again with the active key at each attempt, and the Keyring no longer
-- counts the calls that a key signed.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE gateway_calls
    DROP COLUMN action_data,
    DROP COLUMN button_key_id;

COMMIT;
