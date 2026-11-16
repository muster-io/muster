-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors
-- Adds the Mention entries of the Destinations (C-28.FR-1, FR-5): mention_entries, one ordered list per Destination,
-- each entry one target — everyone in the chat with its word, a User, a messenger group (with a Slack user group's id),
-- a Team, the Teams from the label, the On-call of a Team or of the Teams from the label — and the Loud events it
-- applies to; storm_alert_groups, the Alert Groups each Storm counts, whose Teams the first publication of its Storm
-- summary mentions (C-28.FR-7), since no column links a Storm to the Alert Groups it counts; and the Mention
-- storm_started of the first publication of a Storm summary in delivery_events. Storms started before it have no
-- row, and neither have the Alert Groups that 0.1 replicas count during a rolling update.
-- The conversion of C-28.FR-5: every Destination's L1 Mention settings (destinations.mentions) become entries with the
-- same effect, as mentions.FromSettings converts them in requests until 0.3.0: each target chosen there — the everyone
-- choice other than none, each chosen User, each group, among those the Destination's type took in L1 — becomes one
-- entry for the kinds it was chosen for, with storm_started where new_alert_group is among them, since an L1 Storm
-- summary mentioned the targets of new_alert_group; positions: everyone, then the Users, then the groups, each in the
-- order L1 listed them, kind by kind. A Destination that mentioned nobody gets no entry. A Destination of a Team
-- (team_destinations) without an entry naming that Team gets "Team {Team}" for every Loud event, as C-28.FR-6 adds it,
-- on every type but Telegram; only builds of master between 0032 and this migration have such Destinations.
-- Expand only: destinations.mentions keeps what the migration found and is not written afterwards, except that a
-- Destination created from now on stores the L1 default, nobody, so that a replica of 0.1 that still runs during a
-- rolling update mentions by the column as before and ignores the entries; it never reads or writes the table, and
-- a Destination it saves keeps its entries. delivery_events is partitioned, so its new Mention CHECK is added to the
-- parent NOT VALID, which adds it to every partition, validated in a second transaction, which recurses to every
-- partition, under a lock that lets writes through, and the old CHECK is dropped in a third, as 0010 does for the kind
-- CHECK.

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE mention_entries (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    org_id             bigint NOT NULL REFERENCES organizations (id),
    destination_id     bigint NOT NULL REFERENCES destinations (id) ON DELETE CASCADE,
    position           integer NOT NULL CHECK (position >= 0),
    target_kind        text NOT NULL CHECK (target_kind IN (
                           'everyone', 'user', 'group', 'team', 'teams_from_label', 'on_call_of_team',
                           'on_call_of_teams_from_label')),
    everyone           text CHECK (everyone IN ('channel', 'here', 'all', 'everyone')),
    user_id            bigint REFERENCES users (id),
    group_name         text CHECK (char_length(group_name) > 0),
    slack_usergroup_id text CHECK (slack_usergroup_id ~ '^S[0-9A-Z]+$'),
    team_id            bigint REFERENCES teams (id),
    events             text[] NOT NULL CHECK (events <@ ARRAY[
                           'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended', 'rise_to_urgent',
                           'storm_started']::text[] AND cardinality(events) > 0),
    created_at         timestamptz NOT NULL,
    -- Each kind has exactly its fields.
    CONSTRAINT mention_entries_target_check CHECK (
        (target_kind = 'everyone' AND everyone IS NOT NULL
            AND num_nonnulls(user_id, group_name, slack_usergroup_id, team_id) = 0)
        OR (target_kind = 'user' AND user_id IS NOT NULL
            AND num_nonnulls(everyone, group_name, slack_usergroup_id, team_id) = 0)
        OR (target_kind = 'group' AND group_name IS NOT NULL AND num_nonnulls(everyone, user_id, team_id) = 0)
        OR (target_kind IN ('team', 'on_call_of_team') AND team_id IS NOT NULL
            AND num_nonnulls(everyone, user_id, group_name, slack_usergroup_id) = 0)
        OR (target_kind IN ('teams_from_label', 'on_call_of_teams_from_label')
            AND num_nonnulls(everyone, user_id, group_name, slack_usergroup_id, team_id) = 0)),
    CONSTRAINT mention_entries_position_key UNIQUE (destination_id, position)
);

-- The entries that name a Team or a User.
CREATE INDEX mention_entries_team_idx ON mention_entries (org_id, team_id);
CREATE INDEX mention_entries_user_idx ON mention_entries (org_id, user_id);

-- The Alert Groups a Storm counts: the one that started it and each one that joined it, written with the count.
CREATE TABLE storm_alert_groups (
    org_id         bigint NOT NULL REFERENCES organizations (id),
    storm_id       bigint NOT NULL REFERENCES storms (id) ON DELETE CASCADE,
    alert_group_id bigint NOT NULL REFERENCES alert_groups (id) ON DELETE CASCADE,
    PRIMARY KEY (storm_id, alert_group_id)
);

-- The cascade of the deletion of an Alert Group by retention.
CREATE INDEX storm_alert_groups_alert_group_idx ON storm_alert_groups (alert_group_id);

-- The conversion of the L1 settings: one row per chosen target and kind, in the order of the kinds, then of the lists.
WITH kinds (kind, ord) AS (
    SELECT * FROM unnest(ARRAY['new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
                               'rise_to_urgent']::text[]) WITH ORDINALITY
), settings AS (
    SELECT d.id AS destination_id, d.org_id, d.type, d.updated_at, k.kind, k.ord, d.mentions -> k.kind AS setting
    FROM destinations d
    CROSS JOIN kinds k
    WHERE jsonb_typeof(d.mentions -> k.kind) = 'object'
), chosen AS (
    -- Everyone: Mattermost and outgoing webhooks took channel, all and here, Slack channel and here.
    SELECT s.destination_id, s.org_id, s.updated_at, s.kind, s.ord, 0 AS class, 0::bigint AS idx,
           'everyone' AS target_kind, s.setting ->> 'everyone' AS everyone, NULL::bigint AS user_id,
           NULL::text AS group_name
    FROM settings s
    WHERE (s.type IN ('mattermost', 'webhook') AND s.setting ->> 'everyone' IN ('channel', 'all', 'here'))
       OR (s.type = 'slack' AND s.setting ->> 'everyone' IN ('channel', 'here'))
    UNION ALL
    -- Users: every type took them.
    SELECT s.destination_id, s.org_id, s.updated_at, s.kind, s.ord, 1, u.ord, 'user', NULL, us.id, NULL
    FROM settings s
    CROSS JOIN LATERAL jsonb_array_elements_text(CASE jsonb_typeof(s.setting -> 'user_ids')
                                                     WHEN 'array' THEN s.setting -> 'user_ids'
                                                     ELSE '[]'::jsonb END) WITH ORDINALITY AS u (public_id, ord)
    -- The public_id in the canonical form publicid.Parse gives it.
    JOIN users us ON us.org_id = s.org_id AND us.public_id = translate(upper(u.public_id), 'OIL', '011')
    UNION ALL
    -- Groups: Mattermost and outgoing webhooks took them.
    SELECT s.destination_id, s.org_id, s.updated_at, s.kind, s.ord, 2, g.ord, 'group', NULL, NULL, g.name
    FROM settings s
    CROSS JOIN LATERAL jsonb_array_elements_text(CASE jsonb_typeof(s.setting -> 'groups')
                                                     WHEN 'array' THEN s.setting -> 'groups'
                                                     ELSE '[]'::jsonb END) WITH ORDINALITY AS g (name, ord)
    WHERE s.type IN ('mattermost', 'webhook') AND g.name <> ''
), once AS (
    -- Each target once per kind, as a group or a User L1 listed twice in one kind is one choice.
    SELECT destination_id, org_id, max(updated_at) AS updated_at, kind, ord, class, min(idx) AS idx, target_kind,
           everyone, user_id, group_name
    FROM chosen
    GROUP BY destination_id, org_id, kind, ord, class, target_kind, everyone, user_id, group_name
), targets AS (
    SELECT destination_id, org_id, max(updated_at) AS updated_at, class, target_kind, everyone, user_id, group_name,
           min(ord * 1000000 + idx) AS first,
           array_agg(kind ORDER BY ord) AS kinds
    FROM once
    GROUP BY destination_id, org_id, class, target_kind, everyone, user_id, group_name
)
INSERT INTO mention_entries (org_id, destination_id, position, target_kind, everyone, user_id, group_name, events,
                             created_at)
SELECT org_id, destination_id,
       (row_number() OVER (PARTITION BY destination_id ORDER BY class, first) - 1)::integer,
       target_kind, everyone, user_id, group_name,
       CASE WHEN 'new_alert_group' = ANY(kinds) THEN kinds || 'storm_started'::text ELSE kinds END,
       updated_at
FROM targets;

-- "Team {Team}" on the Destinations of a Team that name it in no entry, after their converted entries, Telegram excepted.
INSERT INTO mention_entries (org_id, destination_id, position, target_kind, team_id, events, created_at)
SELECT td.org_id, td.destination_id,
       (coalesce((SELECT max(e.position) + 1 FROM mention_entries e
                   WHERE e.org_id = td.org_id AND e.destination_id = td.destination_id), 0)
        + row_number() OVER (PARTITION BY td.destination_id ORDER BY td.team_id) - 1)::integer,
       'team', td.team_id,
       ARRAY['new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended', 'rise_to_urgent',
             'storm_started']::text[],
       td.added_at
FROM team_destinations td
JOIN destinations d ON d.org_id = td.org_id AND d.id = td.destination_id
JOIN teams t ON t.org_id = td.org_id AND t.id = td.team_id
WHERE d.type <> 'telegram' AND d.deleted_at IS NULL AND t.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM mention_entries e
                  WHERE e.org_id = td.org_id AND e.destination_id = td.destination_id AND e.target_kind = 'team'
                    AND e.team_id = td.team_id);

ALTER TABLE delivery_events
    ADD CONSTRAINT delivery_events_mentions_v2_check CHECK (mentions <@ ARRAY[
        'owner', 'previous_owner', 'new_alert_group', 'new_alerts', 'reopen', 'ack_timeout', 'snooze_ended',
        'rise_to_urgent', 'storm_started']::text[]) NOT VALID;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE delivery_events VALIDATE CONSTRAINT delivery_events_mentions_v2_check;

COMMIT;

BEGIN;

SET LOCAL lock_timeout = '5s';

ALTER TABLE delivery_events DROP CONSTRAINT delivery_events_mentions_check;

COMMIT;
