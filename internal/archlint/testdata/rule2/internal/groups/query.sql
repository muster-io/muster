-- Good: the groups package writes the Alert Group tables.

-- name: CreateAlertGroup :one
INSERT INTO alert_groups (org_id, number, state) VALUES (@org_id, @number, 'firing') RETURNING id;

-- name: AcknowledgeAlertGroup :exec
UPDATE alert_groups SET state = 'acknowledged' WHERE org_id = @org_id AND id = @id;

-- name: AttachAlert :exec
INSERT INTO alert_group_alerts (org_id, group_id, alert_id) VALUES (@org_id, @group_id, @alert_id);

-- name: AddTimelineEntry :exec
INSERT INTO timeline_entries (org_id, group_id, kind) VALUES (@org_id, @group_id, @kind);

-- name: DeleteNote :exec
DELETE FROM notes WHERE org_id = @org_id AND id = @id;

-- name: NextAlertGroupNumber :one
UPDATE alert_group_counters SET next = next + 1 WHERE org_id = @org_id RETURNING next;

-- name: TouchAlertGroupAlert :exec
MERGE INTO alert_group_alerts t
USING alerts a ON t.alert_id = a.id AND t.org_id = @org_id AND a.org_id = @org_id
WHEN MATCHED THEN UPDATE SET last_seen_at = @at;
