-- Bad: a directory whose name only starts with "groups" is outside internal/groups.

-- name: ResolveAlertGroup :exec
UPDATE alert_groups SET state = 'resolved' WHERE org_id = @org_id AND id = @id; -- want: 2
