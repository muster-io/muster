package groups

import (
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/groups/dbgen/batch"
)

// Good: the groups package calls the queries generated from its own query file.
func Acknowledge(q *dbgen.Queries, orgID, id int64) (batch.Results, error) {
	return batch.Results{}, q.AcknowledgeAlertGroup(orgID, id)
}
