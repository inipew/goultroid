package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/telegram/updates"
	"github.com/inipew/goultroid/internal/database"
)

var ErrUpdateStateNotInitialized = errors.New("telegram update state is not initialized")

// UpdateStateStorage persists gotd update recovery state in Goultroid's SQLite
// database. It retains no process-local cache: gotd owns the in-memory working
// state while SQLite is the restart/recovery authority.
type UpdateStateStorage struct {
	db *database.DB
}

var _ updates.StateStorage = (*UpdateStateStorage)(nil)

func NewUpdateStateStorage(db *database.DB) *UpdateStateStorage {
	return &UpdateStateStorage{db: db}
}

func (s *UpdateStateStorage) InitSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("telegram update state storage database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS telegram_update_state (
			user_id INTEGER PRIMARY KEY,
			pts INTEGER NOT NULL,
			qts INTEGER NOT NULL,
			date INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE TABLE IF NOT EXISTS telegram_update_channel_state (
			user_id INTEGER NOT NULL,
			channel_id INTEGER NOT NULL,
			pts INTEGER NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, channel_id)
		);
		CREATE INDEX IF NOT EXISTS idx_telegram_update_channel_state_user
			ON telegram_update_channel_state(user_id, channel_id);
	`)
	if err != nil {
		return fmt.Errorf("initialize telegram update state schema: %w", err)
	}
	return nil
}

func (s *UpdateStateStorage) GetState(ctx context.Context, userID int64) (updates.State, bool, error) {
	if s == nil || s.db == nil {
		return updates.State{}, false, errors.New("telegram update state storage database is nil")
	}
	var state updates.State
	err := s.db.QueryRowContext(ctx, `
		SELECT pts, qts, date, seq
		FROM telegram_update_state
		WHERE user_id = ?`, userID).Scan(&state.Pts, &state.Qts, &state.Date, &state.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return updates.State{}, false, nil
	}
	if err != nil {
		return updates.State{}, false, fmt.Errorf("get telegram update state for user %d: %w", userID, err)
	}
	return state, true, nil
}

func (s *UpdateStateStorage) SetState(ctx context.Context, userID int64, state updates.State) error {
	if s == nil || s.db == nil {
		return errors.New("telegram update state storage database is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_update_state (user_id, pts, qts, date, seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			pts = excluded.pts,
			qts = excluded.qts,
			date = excluded.date,
			seq = excluded.seq,
			updated_at = excluded.updated_at`,
		userID, state.Pts, state.Qts, state.Date, state.Seq, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("set telegram update state for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) setStateField(ctx context.Context, userID int64, query string, args ...any) error {
	if s == nil || s.db == nil {
		return errors.New("telegram update state storage database is nil")
	}
	params := make([]any, 0, len(args)+2)
	params = append(params, args...)
	params = append(params, time.Now().UTC(), userID)
	result, err := s.db.ExecContext(ctx, query, params...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w: user %d", ErrUpdateStateNotInitialized, userID)
	}
	return nil
}

func (s *UpdateStateStorage) SetPts(ctx context.Context, userID int64, pts int) error {
	if err := s.setStateField(ctx, userID, `UPDATE telegram_update_state SET pts = ?, updated_at = ? WHERE user_id = ?`, pts); err != nil {
		return fmt.Errorf("set telegram pts for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) SetQts(ctx context.Context, userID int64, qts int) error {
	if err := s.setStateField(ctx, userID, `UPDATE telegram_update_state SET qts = ?, updated_at = ? WHERE user_id = ?`, qts); err != nil {
		return fmt.Errorf("set telegram qts for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) SetDate(ctx context.Context, userID int64, date int) error {
	if err := s.setStateField(ctx, userID, `UPDATE telegram_update_state SET date = ?, updated_at = ? WHERE user_id = ?`, date); err != nil {
		return fmt.Errorf("set telegram date for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) SetSeq(ctx context.Context, userID int64, seq int) error {
	if err := s.setStateField(ctx, userID, `UPDATE telegram_update_state SET seq = ?, updated_at = ? WHERE user_id = ?`, seq); err != nil {
		return fmt.Errorf("set telegram seq for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) SetDateSeq(ctx context.Context, userID int64, date, seq int) error {
	if err := s.setStateField(ctx, userID, `UPDATE telegram_update_state SET date = ?, seq = ?, updated_at = ? WHERE user_id = ?`, date, seq); err != nil {
		return fmt.Errorf("set telegram date/seq for user %d: %w", userID, err)
	}
	return nil
}

func (s *UpdateStateStorage) GetChannelPts(ctx context.Context, userID, channelID int64) (int, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, errors.New("telegram update state storage database is nil")
	}
	var pts int
	err := s.db.QueryRowContext(ctx, `
		SELECT pts
		FROM telegram_update_channel_state
		WHERE user_id = ? AND channel_id = ?`, userID, channelID).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get telegram channel pts for user %d channel %d: %w", userID, channelID, err)
	}
	return pts, true, nil
}

func (s *UpdateStateStorage) SetChannelPts(ctx context.Context, userID, channelID int64, pts int) error {
	if s == nil || s.db == nil {
		return errors.New("telegram update state storage database is nil")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_update_channel_state (user_id, channel_id, pts, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, channel_id) DO UPDATE SET
			pts = excluded.pts,
			updated_at = excluded.updated_at`,
		userID, channelID, pts, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("set telegram channel pts for user %d channel %d: %w", userID, channelID, err)
	}
	return nil
}

func (s *UpdateStateStorage) ForEachChannels(ctx context.Context, userID int64, f func(context.Context, int64, int) error) error {
	if s == nil || s.db == nil {
		return errors.New("telegram update state storage database is nil")
	}
	if f == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT channel_id, pts
		FROM telegram_update_channel_state
		WHERE user_id = ?
		ORDER BY channel_id`, userID)
	if err != nil {
		return fmt.Errorf("iterate telegram channel pts for user %d: %w", userID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var channelID int64
		var pts int
		if err := rows.Scan(&channelID, &pts); err != nil {
			return fmt.Errorf("scan telegram channel pts for user %d: %w", userID, err)
		}
		if err := f(ctx, channelID, pts); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate telegram channel pts for user %d: %w", userID, err)
	}
	return nil
}
