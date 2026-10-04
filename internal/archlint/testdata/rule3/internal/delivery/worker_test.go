package delivery

import (
	"context"
	"testing"
)

type fakeAdapter struct{ Adapter }

func (fakeAdapter) Publish(context.Context, Message) (string, error) { return "1", nil }

// Good: tests may call the adapter.
func TestPublish(t *testing.T) {
	var a Adapter = fakeAdapter{}
	if _, err := a.Publish(t.Context(), Message{}); err != nil {
		t.Fatal(err)
	}
}
