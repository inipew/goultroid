package voice

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Queue is a thread-safe FIFO playback queue.
type Queue struct {
	mu     sync.RWMutex
	chatID int64
	tracks []Source
	repo   Repository
}

// NewQueue creates a new Queue instance for a chat. If repo is non-nil,
// pending tracks are restored from the repository under the provided ctx.
func NewQueue(ctx context.Context, chatID int64, repo Repository) (*Queue, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	q := &Queue{
		chatID: chatID,
		tracks: make([]Source, 0),
		repo:   repo,
	}

	// Restore pending tracks from Repository if available
	if repo != nil {
		opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		records, err := repo.GetVoiceQueue(opCtx, chatID)
		if err != nil {
			return nil, fmt.Errorf("restore voice queue: %w", err)
		}
		for _, r := range records {
			q.tracks = append(q.tracks, Source{
				ID:          r.Title,
				Title:       r.Title,
				Artist:      r.Artist,
				Duration:    time.Duration(r.DurationSeconds) * time.Second,
				SourceURL:   r.SourceURL,
				FilePath:    r.FilePath,
				Type:        SourceType(r.SourceType),
				RequesterID: r.RequesterID,
				ChatID:      r.ChatID,
			})
		}
	}

	return q, nil
}

// Enqueue adds a track to the end of the queue.
func (q *Queue) Enqueue(ctx context.Context, track Source) error {
	if ctx == nil {
		return ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.repo != nil {
		opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		err := q.repo.AddVoiceQueueTrack(opCtx, &VoiceQueueRecord{
			ChatID:          track.ChatID,
			Title:           track.Title,
			Artist:          track.Artist,
			SourceURL:       track.SourceURL,
			FilePath:        track.FilePath,
			DurationSeconds: int(track.Duration.Seconds()),
			SourceType:      string(track.Type),
			RequesterID:     track.RequesterID,
			CreatedAt:       time.Now().UTC(),
		})
		if err != nil {
			return fmt.Errorf("persist voice track: %w", err)
		}
	}

	q.tracks = append(q.tracks, track)
	return nil
}

// Dequeue pops the next track from the front of the queue.
func (q *Queue) Dequeue(ctx context.Context) (Source, bool, error) {
	if ctx == nil {
		return Source{}, false, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Source{}, false, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.tracks) == 0 {
		return Source{}, false, nil
	}

	if q.repo != nil {
		opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, err := q.repo.PopVoiceQueueTrack(opCtx, q.chatID)
		if err != nil {
			return Source{}, false, fmt.Errorf("pop persisted voice track: %w", err)
		}
	}

	next := q.tracks[0]
	q.tracks = q.tracks[1:]
	return next, true, nil
}

// Peek views the next track in the queue without removing it.
func (q *Queue) Peek() (Source, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if len(q.tracks) == 0 {
		return Source{}, false
	}
	return q.tracks[0], true
}

// List returns a copy of all currently pending tracks.
func (q *Queue) List() []Source {
	q.mu.RLock()
	defer q.mu.RUnlock()
	copied := make([]Source, len(q.tracks))
	copy(copied, q.tracks)
	return copied
}

// Len returns the current queue length.
func (q *Queue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.tracks)
}

// Clear empties the queue.
func (q *Queue) Clear(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.repo != nil {
		opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := q.repo.ClearVoiceQueue(opCtx, q.chatID); err != nil {
			return fmt.Errorf("clear persisted voice queue: %w", err)
		}
	}

	q.tracks = make([]Source, 0)
	return nil
}

// Shuffle randomly reorders the pending queue.
func (q *Queue) Shuffle() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tracks) <= 1 {
		return
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(q.tracks), func(i, j int) {
		q.tracks[i], q.tracks[j] = q.tracks[j], q.tracks[i]
	})
}
