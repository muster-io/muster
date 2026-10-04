package routing

import (
	"github.com/muster-io/muster/internal/groups/dbgen"       // want: 2
	"github.com/muster-io/muster/internal/groups/dbgen/batch" // want: 2
)

// Bad: routing reaches the Alert Group write queries around the groups dispatcher.
func Acknowledge(q *dbgen.Queries) (batch.Results, error) {
	return batch.Results{}, q.AcknowledgeAlertGroup(1, 1)
}
