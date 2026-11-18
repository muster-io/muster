-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Migration 0051 adds the Pages through Notification gateways of Escalation steps and Notification rules (C-32.FR-3,
-- FR-10, FR-12, C-31.FR-3, C-30.FR-4): each Alert Group of a call names the Page that queued it and the stop check
-- that Page passed — the run of its chain (gateway_call_items.escalation_run_id) and, for a call action of a rule, the
-- rule run (notification_rule_run_id), both ON DELETE SET NULL, and the Page (page_id, ON DELETE CASCADE); a Page of
-- the kind call names its gateway (pages.gateway_connection_id, exactly for a call: pages_gateway_check) and the call
-- it joined (gateway_call_id, ON DELETE SET NULL once the ended call is pruned), whose end settles it; and the reasons
-- of page_not_delivered of a call join those of pages and page_users (pages_reason_v2_check,
-- page_users_reason_v2_check).
-- Expand only: the new columns are nullable, and an earlier binary writes none of them: the items it queues have no
-- stop check of a chain or a rule, and every Page it writes is a Page by Mention. During the window of a rolling
-- update, a replica of that earlier binary that sends a call ends it without settling its Pages and without the stop
-- checks of its items: the Pages of the calls it sent stay pending and make no Assignee. And its delivery worker, whose
-- ListEndedPages has no filter by kind, settles the Pages of calls of this binary as Pages by Mention within a round:
-- it ends them not_delivered with the reason not_delivered, records a wrong page_not_delivered, counts them with the
-- kind mention, and an accepted call then makes no Assignee. As in 0049, the foreign keys and the
-- CHECKs are added NOT VALID in a first transaction, with the indexes of the new columns, validated in a second under
-- a lock that lets writes through, and the old CHECKs dropped in a third.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE gateway_call_items
    ADD COLUMN escalation_run_id bigint,          -- the run of the chain that Paged, its stop check
    ADD COLUMN notification_rule_run_id bigint,   -- the rule run of a call action, its stop check
    ADD COLUMN page_id bigint,                    -- the Page that queued the Alert Group
    ADD CONSTRAINT gateway_call_items_escalation_run_id_fkey FOREIGN KEY (escalation_run_id)
        REFERENCES escalation_runs (id) ON DELETE SET NULL NOT VALID,
    ADD CONSTRAINT gateway_call_items_notification_rule_run_id_fkey FOREIGN KEY (notification_rule_run_id)
        REFERENCES notification_rule_runs (id) ON DELETE SET NULL NOT VALID,
    ADD CONSTRAINT gateway_call_items_page_id_fkey FOREIGN KEY (page_id) REFERENCES pages (id) ON DELETE CASCADE
        NOT VALID;

-- The foreign key actions of the deletion of a run, a rule run and a Page.
CREATE INDEX gateway_call_items_run_idx ON gateway_call_items (escalation_run_id)
    WHERE escalation_run_id IS NOT NULL;
CREATE INDEX gateway_call_items_rule_run_idx ON gateway_call_items (notification_rule_run_id)
    WHERE notification_rule_run_id IS NOT NULL;
CREATE INDEX gateway_call_items_page_idx ON gateway_call_items (page_id) WHERE page_id IS NOT NULL;

ALTER TABLE pages
    ADD COLUMN gateway_connection_id bigint,      -- the Notification gateway of a call
    ADD COLUMN gateway_call_id bigint,            -- the call the Page joined, whose end settles it
    ADD CONSTRAINT pages_gateway_connection_id_fkey FOREIGN KEY (gateway_connection_id) REFERENCES connections (id)
        NOT VALID,
    ADD CONSTRAINT pages_gateway_call_id_fkey FOREIGN KEY (gateway_call_id) REFERENCES gateway_calls (id)
        ON DELETE SET NULL NOT VALID,
    ADD CONSTRAINT pages_gateway_check CHECK ((kind = 'call') = (gateway_connection_id IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT pages_reason_v2_check CHECK (reason IN (
        'no_root_message', 'no_account_link', 'not_delivered', 'call_refused', 'call_unknown', 'call_budget_used',
        'no_confirmed_contact', 'gateway_deleted', 'gateway_broken')) NOT VALID;

-- The Pages a call settles when it ends; it also serves the SET NULL of the pruning of an ended call.
CREATE INDEX pages_gateway_call_idx ON pages (gateway_call_id) WHERE gateway_call_id IS NOT NULL;

ALTER TABLE page_users
    ADD CONSTRAINT page_users_reason_v2_check CHECK (reason IN (
        'no_root_message', 'no_account_link', 'not_delivered', 'call_refused', 'call_unknown', 'call_budget_used',
        'no_confirmed_contact', 'gateway_deleted', 'gateway_broken')) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE gateway_call_items VALIDATE CONSTRAINT gateway_call_items_escalation_run_id_fkey;
ALTER TABLE gateway_call_items VALIDATE CONSTRAINT gateway_call_items_notification_rule_run_id_fkey;
ALTER TABLE gateway_call_items VALIDATE CONSTRAINT gateway_call_items_page_id_fkey;
ALTER TABLE pages VALIDATE CONSTRAINT pages_gateway_connection_id_fkey;
ALTER TABLE pages VALIDATE CONSTRAINT pages_gateway_call_id_fkey;
ALTER TABLE pages VALIDATE CONSTRAINT pages_gateway_check;
ALTER TABLE pages VALIDATE CONSTRAINT pages_reason_v2_check;
ALTER TABLE page_users VALIDATE CONSTRAINT page_users_reason_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE pages DROP CONSTRAINT pages_reason_check;
ALTER TABLE page_users DROP CONSTRAINT page_users_reason_check;

COMMIT;
