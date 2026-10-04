package delivery

import "context"

// Good: the delivery worker reconciles Root messages.
type Worker struct {
	adapter Adapter
}

func (w *Worker) Reconcile(ctx context.Context, id string, msg Message) (string, error) {
	if id == "" {
		return w.adapter.Publish(ctx, msg)
	}
	return id, w.adapter.Update(ctx, id, msg)
}
