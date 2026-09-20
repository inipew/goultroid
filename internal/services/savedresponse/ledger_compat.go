package savedresponse

import (
	"context"

	"github.com/inipew/goultroid/internal/database"
)

// BackfillPersistentMediaLedger performs only the non-destructive P3-A ledger
// backfill from known SavedResponse reference sources. It is safe to run when
// durable storage is unavailable because it neither inspects nor deletes
// physical assets.
func BackfillPersistentMediaLedger(ctx context.Context, db *database.DB, limit int) (int, error) {
	if db == nil {
		return 0, nil
	}
	return newAssetLedger(db).backfillReferences(ctx, limit)
}
