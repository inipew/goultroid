package clone

import (
	"context"
	"github.com/inipew/goultroid/internal/database"
)

type migration001 struct{}

func (migration001) ID() string { return "clone.001" }
func (migration001) Description() string { return "Persistent clone profile snapshots" }
func (migration001) Checksum() string { return "8adfb1432cb5337d924f708b64367e1690a388b47e8291084e40eae20b7a6f22" }
func (migration001) LegacyVersions() []int { return []int{15} }
func (migration001) Up(ctx context.Context, tx interface{ ExecContext(context.Context, string, ...any) (interface{ LastInsertId() (int64, error); RowsAffected() (int64, error) }, error) }) error { return nil }

var _ database.Migration = migration001{}
