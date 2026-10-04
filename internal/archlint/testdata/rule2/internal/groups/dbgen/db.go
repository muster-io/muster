// Package dbgen stands in for the queries sqlc generates from internal/groups/query.sql.
package dbgen

type Queries struct{}

func (q *Queries) AcknowledgeAlertGroup(orgID, id int64) error { return nil }
