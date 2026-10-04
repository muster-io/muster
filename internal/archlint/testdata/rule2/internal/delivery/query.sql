-- Reads of the Alert Group tables and writes of delivery's own table are good; each "want: 2" marks a write that
-- rule 2 must report.

-- name: GetAlertGroupForRender :one
SELECT id, state FROM alert_groups WHERE org_id = @org_id AND id = @id;

-- name: RecordDeliveryEvent :exec
INSERT INTO delivery_events (org_id, group_id, kind)
SELECT org_id, id, @kind FROM alert_groups WHERE org_id = @org_id AND id = @group_id;

-- name: SetAlertGroupState :exec
UPDATE alert_groups SET state = @state WHERE org_id = @org_id AND id = @id; -- want: 2

-- name: SetAlertGroupStateQualified :exec
UPDATE public.alert_groups SET state = @state WHERE org_id = @org_id AND id = @id; -- want: 2

-- name: AddTimelineEntryFromDelivery :exec
INSERT INTO timeline_entries (org_id, group_id, kind) VALUES (@org_id, @group_id, 'delivered'); -- want: 2

-- name: DeleteNotes :exec
DELETE FROM notes WHERE org_id = @org_id AND group_id = @group_id; -- want: 2

-- name: BumpCounterInCTE :one
WITH bumped AS (
    UPDATE alert_group_counters SET next = next + 1 WHERE org_id = @org_id RETURNING next -- want: 2
)
SELECT next FROM bumped;

-- name: MergeAlertGroupAlerts :exec
MERGE INTO alert_group_alerts t -- want: 2
USING alerts a ON t.alert_id = a.id AND t.org_id = @org_id AND a.org_id = @org_id
WHEN MATCHED THEN DELETE;
