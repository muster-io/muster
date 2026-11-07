package delivery

import "context"

// Good: the interactive path answers people.
func Answer(ctx context.Context, a Adapter, id string, msg Message) error {
	return a.Update(ctx, id, msg)
}

// Good: the interactive path sends the confirmation code of a contact.
func SendCode(ctx context.Context, g Gateway, n Notification) error {
	return g.Notify(ctx, n)
}
