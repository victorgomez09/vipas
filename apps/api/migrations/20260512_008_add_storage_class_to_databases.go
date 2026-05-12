package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		fmt.Println("running migration 20260512_008_add_storage_class_to_databases (up)")
		_, err := db.ExecContext(ctx, `
			ALTER TABLE managed_databases ADD COLUMN IF NOT EXISTS storage_class TEXT NOT NULL DEFAULT 'local-path';
		`)
		return err
	}, func(ctx context.Context, db *bun.DB) error {
		fmt.Println("running migration 20260512_008_add_storage_class_to_databases (down)")
		_, err := db.ExecContext(ctx, `
			ALTER TABLE managed_databases DROP COLUMN IF EXISTS storage_class;
		`)
		return err
	})
}
