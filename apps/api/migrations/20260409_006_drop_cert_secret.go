package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		// Drop cert_secret column if present
		if _, err := db.ExecContext(ctx, `ALTER TABLE domains DROP COLUMN IF EXISTS cert_secret`); err != nil {
			return fmt.Errorf("drop cert_secret column: %w", err)
		}
		return nil
	}, func(ctx context.Context, db *bun.DB) error {
		// Rollback: re-create cert_secret column as text (best-effort)
		if _, err := db.ExecContext(ctx, `ALTER TABLE domains ADD COLUMN IF NOT EXISTS cert_secret text`); err != nil {
			return fmt.Errorf("rollback add cert_secret column: %w", err)
		}
		return nil
	})
}
