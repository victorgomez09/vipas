package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		// Rename ingress_ready -> route_ready if present
		if _, err := db.ExecContext(ctx, `ALTER TABLE domains RENAME COLUMN IF EXISTS ingress_ready TO route_ready`); err != nil {
			return fmt.Errorf("rename ingress_ready -> route_ready: %w", err)
		}
		return nil
	}, func(ctx context.Context, db *bun.DB) error {
		// Rollback: rename back
		if _, err := db.ExecContext(ctx, `ALTER TABLE domains RENAME COLUMN IF EXISTS route_ready TO ingress_ready`); err != nil {
			return fmt.Errorf("rollback rename route_ready -> ingress_ready: %w", err)
		}
		return nil
	})
}
