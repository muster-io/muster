package delivery

import "context"

// Bad: the delivery package, but not one of the allowed files.
func (w *Worker) Retry(ctx context.Context, msg Message) error {
	_, err := w.adapter.Publish(ctx, msg) // want: 3
	return err
}
