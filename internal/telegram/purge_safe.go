package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

const (
	// SafePurgeMaxMessages is a hard safety ceiling for one purge operation.
	SafePurgeMaxMessages = 1000
	// SafePurgePageSize keeps Telegram history/reply requests bounded.
	SafePurgePageSize = 100
)

// PurgeMessagesSafe deletes only concrete message IDs discovered from the target
// chat/thread. It deliberately does not infer deletions from an ID interval alone.
// For forum topics, MessagesGetReplies is used with the exact topic root, which
// prevents messages belonging to other topics from entering the delete set.
func (s *Service) PurgeMessagesSafe(ctx context.Context, peer tg.InputPeerClass, topicID, fromID, toID int) (int, error) {
	if s == nil || s.api == nil {
		return 0, errors.New("api is not initialized")
	}
	if peer == nil {
		return 0, errors.New("peer is nil")
	}
	if fromID <= 0 || toID <= 0 {
		return 0, fmt.Errorf("invalid purge message IDs: from=%d to=%d", fromID, toID)
	}
	if fromID >= toID {
		return 0, fmt.Errorf("invalid purge range: start message must be older than command (from=%d to=%d)", fromID, toID)
	}

	// Never delete the command itself. The purge handler intentionally edits the
	// command into the success/error result; deleting it first causes MESSAGE_ID_INVALID.
	maxID := toID - 1
	minID := fromID
	ids := make(map[int]struct{})
	offsetID := maxID + 1

	for len(ids) < SafePurgeMaxMessages {
		var messages []tg.MessageClass
		var err error

		if topicID > 0 {
			_, err = retryOnFloodWait(ctx, func() (struct{}, error) {
				resp, callErr := s.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
					Peer:     peer,
					MsgID:    topicID,
					OffsetID: offsetID,
					MinID:    minID,
					MaxID:    maxID,
					Limit:    SafePurgePageSize,
				})
				if callErr != nil {
					return struct{}{}, callErr
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return struct{}{}, nil
			})
		} else {
			_, err = retryOnFloodWait(ctx, func() (struct{}, error) {
				resp, callErr := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
					Peer:     peer,
					OffsetID: offsetID,
					MinID:    minID,
					MaxID:    maxID,
					Limit:    SafePurgePageSize,
				})
				if callErr != nil {
					return struct{}{}, callErr
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return struct{}{}, nil
			})
		}
		if err != nil {
			return 0, fmt.Errorf("failed to inspect purge range: %w", mapTelegramError(err))
		}
		if len(messages) == 0 {
			break
		}

		lowest := offsetID
		newIDs := 0
		for _, item := range messages {
			msg, ok := item.(*tg.Message)
			if !ok || msg == nil || msg.ID < minID || msg.ID > maxID {
				continue
			}
			if msg.ID < lowest {
				lowest = msg.ID
			}
			if _, exists := ids[msg.ID]; !exists {
				ids[msg.ID] = struct{}{}
				newIDs++
			}
		}

		if newIDs == 0 || lowest >= offsetID || lowest <= minID {
			break
		}
		offsetID = lowest
	}

	if len(ids) == 0 {
		return 0, nil
	}

	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	// Stable ascending order makes logs/tests deterministic and simplifies retry diagnostics.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j] < ordered[j-1]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	deleted := 0
	for i := 0; i < len(ordered); i += 100 {
		end := i + 100
		if end > len(ordered) {
			end = len(ordered)
		}
		if err := s.DeleteMessage(ctx, peer, ordered[i:end]); err != nil {
			return deleted, fmt.Errorf("purge delete failed after %d messages: %w", deleted, mapTelegramError(err))
		}
		deleted += end - i
	}
	return deleted, nil
}
