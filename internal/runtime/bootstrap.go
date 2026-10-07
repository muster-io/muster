// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package runtime

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/users"
)

// ensureStep is one start-up "ensure" step: it creates the runtime rows of a capability when they are missing and
// changes nothing that exists, so it runs at every start and a database created by an earlier version gains the rows
// of later capabilities. Steps run in order inside the migration lock, after the migrations and before serving.
type ensureStep struct {
	name string
	run  func(context.Context, *process) error
}

// ensureSteps grow with the capabilities: the built-in Integration and the Default route follow the Organization, and
// the built-in Link rule (S-037) will follow them. The partitions that the next days and months need are created
// before serving, so that no write finds its partition missing; the bootstrap Admin comes after them, because its
// creation writes the Audit log.
var ensureSteps = []ensureStep{
	{name: "organization", run: func(ctx context.Context, p *process) error {
		return organization.Ensure(ctx, p.db.OrganizationStore(), p.log, p.clocks.Business.Now())
	}},
	{name: "builtin integration", run: func(ctx context.Context, p *process) error {
		orgID, err := p.organizationID(ctx)
		if err != nil {
			return err
		}
		return integrations.EnsureBuiltin(ctx, p.db.IntegrationsStore(), orgID, p.clocks.Business.Now())
	}},
	{name: "default route", run: func(ctx context.Context, p *process) error {
		orgID, err := p.organizationID(ctx)
		if err != nil {
			return err
		}
		return routing.EnsureDefault(ctx, p.db.RoutingStore(), orgID, p.clocks.Business.Now())
	}},
	{name: "partitions", run: func(ctx context.Context, p *process) error {
		return p.partitions.Create(ctx)
	}},
	{name: "bootstrap admin", run: func(ctx context.Context, p *process) error {
		orgID, err := p.organizationID(ctx)
		if err != nil {
			return err
		}
		return users.EnsureBootstrapAdmin(ctx, p.db.UsersStore(), audit.NewWriter(p.log, p.clocks.Business), p.log,
			orgID, users.Bootstrap{Email: p.cfg.BootstrapAdminEmail, Password: p.cfg.BootstrapAdminPassword},
			p.clocks.Business.Now())
	}},
}

// bootstrap runs under the migration lock: it writes the active key and the key canary on a new database, checks the
// canary with the Keyring, then runs the ensure steps. A Keyring that cannot decrypt the canary stops it with
// keyring.ErrKeyMismatch before any step runs.
func (p *process) bootstrap(ctx context.Context) error {
	return p.db.WithMigrationLock(ctx, func(ctx context.Context) error {
		st, err := p.keyring.Establish(ctx, p.db.KeyringStore(), p.clocks.Business.Now())
		if err != nil {
			return err
		}
		if err := p.keyring.Open(ctx, p.log, st); err != nil {
			return err
		}
		for _, s := range ensureSteps {
			if err := s.run(ctx, p); err != nil {
				return fmt.Errorf("start-up step %s: %w", s.name, err)
			}
		}
		return nil
	})
}

// startReplica writes this replica's key record; the record is refreshed while serving and deleted at shutdown.
func (p *process) startReplica(ctx context.Context) error {
	host, _ := os.Hostname() // an empty host name still gives a unique replica id
	p.replica = keyring.NewRecorder(p.keyring, p.db.KeyringStore(), p.clocks.Real, p.log,
		keyring.NewReplicaID(host), host, buildinfo.Version)
	return p.replica.Start(ctx)
}

// watchKeys refreshes the replica record every replica.key_record_refresh until ctx ends; it returns
// keyring.ErrKeyMismatch when the active key is one this replica does not hold.
func (p *process) watchKeys(ctx context.Context) error {
	refresh := p.opts.keyRecordRefresh
	if refresh == 0 {
		refresh = keyring.KeyRecordRefresh
	}
	t := time.NewTicker(refresh)
	defer t.Stop()
	return p.replica.Run(ctx, t.C)
}

// stopReplica deletes this replica's record at a graceful shutdown.
func (p *process) stopReplica(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := p.replica.Stop(ctx); err != nil {
		p.log.Log(ctx, logging.ReplicaRecordFailed, logging.F("replica", p.replica.ID()), logging.F("error", err.Error()))
	}
}

// organizationID is the internal id of the single Organization, which the ensure step created.
func (p *process) organizationID(ctx context.Context) (int64, error) {
	org, err := p.db.OrganizationStore().GetOrganization(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the organization: %w", err)
	}
	return org.ID, nil
}
