package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		fmt.Println("running migration 20260513_010_add_longhorn_replicas_to_applications (up)")
		_, err := db.ExecContext(ctx, `
			ALTER TABLE applications ADD COLUMN IF NOT EXISTS longhorn_replicas INTEGER NOT NULL DEFAULT 3;
		`)
		return err
	}, func(ctx context.Context, db *bun.DB) error {
		fmt.Println("running migration 20260513_010_add_longhorn_replicas_to_applications (down)")
		_, err := db.ExecContext(ctx, `
			ALTER TABLE applications DROP COLUMN IF EXISTS longhorn_replicas;
		`)
		return err
	})
}
