package groups

import (
	"context"

	"github.com/muster-io/muster/internal/delivery"
)

// Good: methods named like the adapter's on types that are not messenger adapters.
type store struct{}

func (store) Update(ctx context.Context, id string) error {
	return nil
}

type updater interface {
	Update(ctx context.Context, id string) error
}

func Save(ctx context.Context, u updater) error {
	if err := u.Update(ctx, "1"); err != nil {
		return err
	}
	return store{}.Update(ctx, "1")
}

type namer interface {
	Name() string
}

func Describe(n namer, msg delivery.Message) string {
	return n.Name() + msg.Text
}
