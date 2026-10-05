package delivery

import "context"

// Good: thread replies belong to the delivery worker.
func (w *Worker) ReplyInThread(ctx context.Context, id string, msg Message) (string, error) {
	return w.adapter.Reply(ctx, id, msg)
}
