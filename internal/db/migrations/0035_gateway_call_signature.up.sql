-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0035 keeps the signed action data of a call through a Notification gateway (C-30.FR-2, FR-6): from its
-- first attempt, gateway_calls.action_data holds the action data that the button-signature key signed for it, and
-- button_key_id the id of that key, so that every retry sends the same data and the Keyring keeps the key while the
-- gateway may still report it (C-20.FR-6). The data is cleared when the call ends, as its text is, and the key id
-- stays; a call that a replica of an earlier version ends during a rolling update keeps its data until the retention
-- of its Alert Groups deletes it.
-- Expand only: a replica of an earlier version that still runs during a rolling update writes neither column, which
-- are nullable, and signs the data of each attempt with the active key, as before; a call it started has neither, and
-- a later version signs that call's attempts as it did.

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE gateway_calls
    ADD COLUMN action_data text,      -- the signed action data of the call, from its first attempt until it ends
    ADD COLUMN button_key_id text;    -- the button-signature key that signed it

COMMIT;
