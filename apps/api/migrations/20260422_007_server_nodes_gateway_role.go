package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		if _, err := db.ExecContext(ctx, `ALTER TABLE server_nodes DROP CONSTRAINT IF EXISTS server_nodes_role_check`); err != nil {
			return fmt.Errorf("drop old role constraint: %w", err)
		}
		if _, err := db.ExecContext(ctx, `
			ALTER TABLE server_nodes
			ADD CONSTRAINT server_nodes_role_check
			CHECK (role IN ('worker', 'server', 'control-plane', 'gateway'))
		`); err != nil {
			return fmt.Errorf("add server_nodes role constraint: %w", err)
		}
		return nil
	}, func(ctx context.Context, db *bun.DB) error {
		if _, err := db.ExecContext(ctx, `ALTER TABLE server_nodes DROP CONSTRAINT IF EXISTS server_nodes_role_check`); err != nil {
			return fmt.Errorf("rollback drop role constraint: %w", err)
		}
		if _, err := db.ExecContext(ctx, `
			ALTER TABLE server_nodes
			ADD CONSTRAINT server_nodes_role_check
			CHECK (role IN ('worker', 'server'))
		`); err != nil {
			return fmt.Errorf("rollback add previous role constraint: %w", err)
		}
		return nil
	})
}
