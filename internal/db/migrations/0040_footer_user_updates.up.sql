-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds three kinds of open_group_updates rows that re-render the open Root messages after a change of the user their
-- footer names (C-12.FR-12): user_name, which a change of a User's display name inserts — a profile update, an update by
-- an Admin, and the pseudonym of a deletion — with the User in the new column user_id; account_links, which a new,
-- replaced, changed or removed Account link of a User inserts, also with the User in user_id; and service_account_name,
-- which a rename of a Service account inserts, with it in the new column service_account_id. The run of user_name or
-- account_links walks the open Alert Groups whose footer names the User — their Owner while acknowledged, who snoozed
-- them while snoozed — and that of service_account_name those a Service account snoozed; each only re-renders, so that
-- delivery edits, Quietly, the Root messages whose Desired state changed and leaves the others, and a second run edits
-- nothing. A change writes the row even while no open Alert Group names the person: a Snapshot in flight may give one
-- back its Owner or snoozer, which the run waits for under the lock of the evaluation; otherwise the run edits nothing.
-- Users and Service accounts are deleted softly, so the foreign keys never meet a deleted row; should one ever be
-- removed, its pending re-render goes with it (ON DELETE CASCADE), which keeps the scope CHECKs true. The indexes on who
-- snoozed an Alert Group, partial on the open ones, serve the walks; alert_groups_owner_idx serves the Owner, and holds
-- only open Alert Groups already, since alert_groups_owner_check sets owner_user_id only while acknowledged. The kind
-- CHECK is replaced by open_group_updates_kind_v8_check with every kind of _v7 (0039) and the three new ones.
-- Expand only: a replica of 0.1, or of a development image of 0.2 without this migration, that still runs during a
-- rolling update never writes or claims rows of the new kinds — a rename or an Account link it saves re-renders
-- nothing, and its messages catch up at each Alert Group's next change — and a row a newer replica writes waits for a
-- replica that knows its kind. The indexes are built in a first transaction of their own, so that open_group_updates
-- is not held while they build, which holds the writes to alert_groups, ingestion included, for as long as the build
-- runs; the new columns, foreign keys and CHECKs are added NOT VALID in a second, validated in
-- a third under a lock that lets writes through, and the old kind CHECK is dropped in a fourth, as 0039 does.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX alert_groups_snoozed_by_user_idx ON alert_groups (org_id, snoozed_by_user_id)
    WHERE snoozed_by_user_id IS NOT NULL AND status <> 'resolved';
CREATE INDEX alert_groups_snoozed_by_service_account_idx ON alert_groups (org_id, snoozed_by_service_account_id)
    WHERE snoozed_by_service_account_id IS NOT NULL AND status <> 'resolved';

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates
    ADD COLUMN user_id bigint,
    ADD COLUMN service_account_id bigint,
    ADD CONSTRAINT open_group_updates_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
        NOT VALID,
    ADD CONSTRAINT open_group_updates_service_account_id_fkey FOREIGN KEY (service_account_id)
        REFERENCES service_accounts (id) ON DELETE CASCADE NOT VALID,
    ADD CONSTRAINT open_group_updates_kind_v8_check
        CHECK (kind IN ('route_minimum_severity', 'destination_buttons', 'organization_buttons', 'team_evaluation',
                        'route_message_labels', 'organization_message_settings', 'team_name', 'team_destinations',
                        'route_message_settings', 'organization_links', 'user_name', 'account_links',
                        'service_account_name')) NOT VALID,
    ADD CONSTRAINT open_group_updates_user_scope_check
        CHECK (kind NOT IN ('user_name', 'account_links') OR user_id IS NOT NULL) NOT VALID,
    ADD CONSTRAINT open_group_updates_service_account_scope_check
        CHECK (kind <> 'service_account_name' OR service_account_id IS NOT NULL) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_user_id_fkey;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_service_account_id_fkey;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_kind_v8_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_user_scope_check;
ALTER TABLE open_group_updates VALIDATE CONSTRAINT open_group_updates_service_account_scope_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE open_group_updates DROP CONSTRAINT open_group_updates_kind_v7_check;

COMMIT;
