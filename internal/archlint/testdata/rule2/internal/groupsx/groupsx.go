package groupsx

import "github.com/muster-io/muster/internal/groups/dbgen" // want: 2

// Bad: a directory whose name only starts with "groups" is outside internal/groups.
var queries dbgen.Queries
