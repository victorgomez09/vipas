package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		// Rename ingress_ready -> route_ready if present (Postgres doesn't support IF EXISTS on RENAME COLUMN)
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='domains' AND column_name='ingress_ready')`).Scan(&exists); err != nil {
			return fmt.Errorf("check ingress_ready existence: %w", err)
		}
		if exists {
			if _, err := db.ExecContext(ctx, `ALTER TABLE domains RENAME COLUMN ingress_ready TO route_ready`); err != nil {
				return fmt.Errorf("rename ingress_ready -> route_ready: %w", err)
			}
		}
		return nil
	}, func(ctx context.Context, db *bun.DB) error {
		// Rollback: rename back if route_ready exists
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='domains' AND column_name='route_ready')`).Scan(&exists); err != nil {
			return fmt.Errorf("check route_ready existence: %w", err)
		}
		if exists {
			if _, err := db.ExecContext(ctx, `ALTER TABLE domains RENAME COLUMN route_ready TO ingress_ready`); err != nil {
				return fmt.Errorf("rollback rename route_ready -> ingress_ready: %w", err)
			}
		}
		return nil
	})
}
