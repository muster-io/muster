package delivery

import "context"

// Good: the interactive path answers people.
func Answer(ctx context.Context, a Adapter, id string, msg Message) error {
	return a.Update(ctx, id, msg)
}
