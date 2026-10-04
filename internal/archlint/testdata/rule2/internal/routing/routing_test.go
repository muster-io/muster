package routing

import (
	"testing"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// Good: tests may import the generated queries.
func TestAcknowledge(t *testing.T) {
	if _, err := Acknowledge(&dbgen.Queries{}); err != nil {
		t.Fatal(err)
	}
}
