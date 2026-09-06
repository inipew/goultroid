package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
)

const (
	SafePurgeMaxMessages = 1000
	SafePurgePageSize    = 100
	SafePurgeDeleteBatch = 100
)

// PurgeMessagesSafe deletes only concrete message IDs discovered from the target
// chat/thread. For forum topics, it queries the exact topic root rather than the
// whole group history.
//
// Safety invariants:
//   - the command message is never included in the deletion set;
//   - the replied-to message IS included in the deletion range;
//   - a forum topic is queried by its exact topic root;
//   - the forum topic root itself is never deleted;
//   - only concrete Message IDs returned by Telegram are deleted;
//   - a hard ceiling prevents an accidental large-range purge;
//   - MESSAGE_ID_INVALID during deletion is treated as idempotent success because
//     the message may have been removed concurrently after discovery.
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
	if topicID < 0 {
		return 0, fmt.Errorf("invalid forum topic ID: %d", topicID)
	}
	if topicID > 0 && topicID >= toID {
		return 0, fmt.Errorf("invalid forum topic ID %d for command message %d", topicID, toID)
	}

	// Telegram's MinID is a lower boundary for history/replies rather than an
	// inclusive message selector (Telegram returns id > MinID). Query one ID below
	// the requested start so the replied-to message is discovered.
	// Similarly, Telegram's MaxID returns id < MaxID. Passing toID allows
	// Telegram to return messages up to toID - 1 (including message e), while
	// excluding toID (the command trigger message itself).
	queryMinID, queryMaxID := safePurgeQueryBounds(fromID, toID)
	localMaxID := toID - 1
	ids := make(map[int]struct{}, min(maxInt(SafePurgeMaxMessages, SafePurgePageSize), localMaxID-fromID+1))
	offsetID := toID

	for len(ids) < SafePurgeMaxMessages {
		var messages []tg.MessageClass
		var err error

		if topicID > 0 {
			_, err = retryOnFloodWait(ctx, func() (struct{}, error) {
				resp, callErr := s.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
					Peer:     peer,
					MsgID:    topicID,
					OffsetID: offsetID,
					MinID:    queryMinID,
					MaxID:    queryMaxID,
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
					MinID:    queryMinID,
					MaxID:    queryMaxID,
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

		lowest, newIDs := collectSafePurgeIDs(messages, fromID, localMaxID, topicID, ids, offsetID)

		// Telegram pagination must make monotonic progress. If a response cannot
		// move the cursor, stop rather than risking a repeated query loop.
		if lowest >= offsetID {
			break
		}
		if lowest <= queryMinID {
			break
		}
		// offset_id is exclusive on the older side, so reusing the lowest ID
		// moves to the next older page without skipping that lowest message.
		offsetID = lowest

		// If the page contained no new IDs but still moved through unrelated
		// messages, continue pagination until the requested lower boundary.
		if newIDs == 0 && lowest <= fromID {
			break
		}
	}

	if len(ids) == 0 {
		return 0, nil
	}

	ordered := make([]int, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	// Small bounded set (<=1000): insertion sort avoids another dependency and
	// keeps deletion order deterministic for easier debugging/auditing.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j] < ordered[j-1]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	deleted := 0
	for i := 0; i < len(ordered); i += SafePurgeDeleteBatch {
		end := i + SafePurgeDeleteBatch
		if end > len(ordered) {
			end = len(ordered)
		}

		if err := s.DeleteMessage(ctx, peer, ordered[i:end]); err != nil {
			// Purge is intentionally idempotent. Another moderator, an
			// auto-delete policy, or a concurrent purge may have removed the
			// messages between discovery and deletion.
			if isAlreadyDeletedMessageError(err) {
				deleted += end - i
				continue
			}
			return deleted, fmt.Errorf("purge delete failed after %d messages: %w", deleted, mapTelegramError(err))
		}
		deleted += end - i
	}
	return deleted, nil
}

// safePurgeQueryBounds converts the user-visible inclusive start / exclusive
// command range into Telegram history/replies query bounds.
// Telegram's MinID returns messages with id > minID.
// Telegram's MaxID returns messages with id < maxID.
// Therefore, to query messages in [fromID, toID):
//   - minID must be fromID - 1 so id > fromID - 1 => id >= fromID (includes replied-to message);
//   - maxID must be toID so id < toID => id <= toID - 1 (includes message immediately preceding command, excludes command).
func safePurgeQueryBounds(fromID, toID int) (minID, maxID int) {
	minID = fromID - 1
	if minID < 0 {
		minID = 0
	}
	return minID, toID
}

// collectSafePurgeIDs applies the final inclusive [fromID, maxID] filter to one
// Telegram page and appends concrete message IDs to the bounded deletion set.
// It is intentionally shared by production code and regression tests so the
// reply-boundary invariant cannot silently drift.
func collectSafePurgeIDs(messages []tg.MessageClass, fromID, maxID, topicID int, ids map[int]struct{}, offsetID int) (lowest, newIDs int) {
	lowest = offsetID
	for _, item := range messages {
		msg, ok := item.(*tg.Message)
		if !ok || msg == nil || msg.ID < fromID || msg.ID > maxID {
			continue
		}

		// Deleting a forum topic root is never part of a safe purge. A topic
		// purge removes replies within the topic, not the topic container.
		if topicID > 0 && msg.ID == topicID {
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
	return lowest, newIDs
}

func isAlreadyDeletedMessageError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToUpper(err.Error())
	return strings.Contains(text, "MESSAGE_ID_INVALID") || strings.Contains(text, "MESSAGE_NOT_MODIFIED")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
