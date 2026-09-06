package voice

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// Queue is a thread-safe FIFO playback queue.
type Queue struct {
	mu     sync.RWMutex
	chatID int64
	tracks []Source
	db     *database.DB
}

// NewQueue creates a new Queue instance for a chat.
func NewQueue(chatID int64, db *database.DB) *Queue {
	q := &Queue{
		chatID: chatID,
		tracks: make([]Source, 0),
		db:     db,
	}

	// Restore pending tracks from DB if available
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if records, err := db.GetVoiceQueue(ctx, chatID); err == nil {
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
	}

	return q
}

// Enqueue adds a track to the end of the queue.
func (q *Queue) Enqueue(track Source) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = append(q.tracks, track)

	if q.db != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = q.db.AddVoiceQueueTrack(ctx, &database.VoiceQueueRecord{
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
		}()
	}
}

// Dequeue pops the next track from the front of the queue.
func (q *Queue) Dequeue() (Source, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.tracks) == 0 {
		return Source{}, false
	}

	next := q.tracks[0]
	q.tracks = q.tracks[1:]

	if q.db != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = q.db.PopVoiceQueueTrack(ctx, q.chatID)
		}()
	}

	return next, true
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
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tracks = make([]Source, 0)

	if q.db != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = q.db.ClearVoiceQueue(ctx, q.chatID)
		}()
	}
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
