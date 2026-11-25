package myxl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/userlog"
)

const maintenanceNotifyTotalBudget = 15 * time.Second

// NotificationEvent represents the maintenance lifecycle event routed to destinations.
type NotificationEvent string

const (
	EventNone           NotificationEvent = ""
	EventCompletion     NotificationEvent = "completion"
	EventPartialFailure NotificationEvent = "partial_failure"
	EventFailure        NotificationEvent = "failure"
	EventCancelled      NotificationEvent = "cancelled"
)

// DeliveryOutcomeKind classifies the per-destination delivery result.
type DeliveryOutcomeKind string

const (
	DeliverySent             DeliveryOutcomeKind = "sent"
	DeliveryFailed           DeliveryOutcomeKind = "failed"
	DeliveryInvalidDest      DeliveryOutcomeKind = "invalid_destination"
	DeliverySkippedDisabled  DeliveryOutcomeKind = "skipped_disabled"
	DeliverySkippedDeadline  DeliveryOutcomeKind = "skipped_deadline"
	DeliverySkippedCancelled DeliveryOutcomeKind = "skipped_cancelled"
)

// DestinationDeliveryResult records one bounded, non-secret delivery outcome.
type DestinationDeliveryResult struct {
	DestinationID string
	Type          NotificationDestinationType
	Outcome       DeliveryOutcomeKind
	Reason        string
}

// NotificationBatchResult summarizes one sequential multi-destination delivery pass.
type NotificationBatchResult struct {
	Event        NotificationEvent
	Total        int
	Sent         int
	Failed       int
	InvalidDest  int
	Skipped      int
	Duration     time.Duration
	PerRecipient []DestinationDeliveryResult
}

var (
	errDestinationInvalid = errors.New("invalid notification destination")
	errGlobalLogMissing   = errors.New("global log destination is not configured")
)

// SetNotificationServices wires optional PeerResolver and UserlogService for
// multi-destination notification resolution.
func (p *Plugin) SetNotificationServices(resolver core.PeerResolver, ulog *userlog.Service) {
	if p == nil {
		return
	}
	p.maintMu.Lock()
	p.peerResolver = resolver
	p.userlogSvc = ulog
	p.maintMu.Unlock()
}

// classifyMaintenanceNotificationEvent maps a MaintenanceRunSummary to a single
// NotificationEvent and reports whether notification is enabled for that event.
// no_op (RunStatusCompleted with RefreshedCount == 0) never triggers notification.
func classifyMaintenanceNotificationEvent(cfg TokenMaintenanceConfig, summary *MaintenanceRunSummary) (NotificationEvent, bool) {
	if summary == nil {
		return EventNone, false
	}
	switch summary.Status {
	case RunStatusCompleted:
		if summary.RefreshedCount == 0 {
			return EventNone, false
		}
		return EventCompletion, cfg.NotifyOnCompletion
	case RunStatusPartialFailure:
		return EventPartialFailure, cfg.NotifyOnPartialFailure
	case RunStatusFailed:
		return EventFailure, cfg.NotifyOnFailure
	case RunStatusCancelled:
		return EventCancelled, cfg.NotifyOnCancelled
	default:
		return EventNone, false
	}
}

// effectiveNotificationDestinations returns enabled destinations in order, or the
// implicit Owner PM fallback when no explicit destinations are configured.
func effectiveNotificationDestinations(cfg TokenMaintenanceConfig) []NotificationDestination {
	if len(cfg.Destinations) == 0 {
		return []NotificationDestination{
			{ID: "owner", Type: DestOwner, Label: "Owner PM", Enabled: true},
		}
	}
	enabled := make([]NotificationDestination, 0, len(cfg.Destinations))
	for _, d := range cfg.Destinations {
		if d.Enabled {
			enabled = append(enabled, d)
		}
	}
	return enabled
}

// resolveNotificationPeer converts a validated NotificationDestination into its
// semantically accurate tg.InputPeerClass without fabricating AccessHash values.
func resolveNotificationPeer(
	ctx context.Context,
	dest NotificationDestination,
	ownerID int64,
	resolver core.PeerResolver,
	ulog *userlog.Service,
) (tg.InputPeerClass, error) {
	switch dest.Type {
	case DestOwner:
		if ownerID <= 0 {
			return nil, fmt.Errorf("%w: runtime owner id is unavailable", errDestinationInvalid)
		}
		return &tg.InputPeerUser{UserID: ownerID}, nil

	case DestGroup:
		if dest.PeerID <= 0 {
			return nil, fmt.Errorf("%w: group peer_id is invalid", errDestinationInvalid)
		}
		return &tg.InputPeerChat{ChatID: dest.PeerID}, nil

	case DestSupergroup, DestChannel:
		if dest.PeerID <= 0 || dest.AccessHash == 0 {
			return nil, fmt.Errorf("%w: %s requires positive peer_id and access_hash", errDestinationInvalid, dest.Type)
		}
		return &tg.InputPeerChannel{ChannelID: dest.PeerID, AccessHash: dest.AccessHash}, nil

	case DestBotPM:
		if dest.PeerID <= 0 || dest.AccessHash == 0 {
			return nil, fmt.Errorf("%w: bot_pm requires positive peer_id and access_hash", errDestinationInvalid)
		}
		return &tg.InputPeerUser{UserID: dest.PeerID, AccessHash: dest.AccessHash}, nil

	case DestGlobalLog:
		if ulog == nil {
			return nil, fmt.Errorf("%w: userlog service unavailable", errGlobalLogMissing)
		}
		logDest, err := ulog.GetDestination(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: read global log destination: %w", errGlobalLogMissing, err)
		}
		if logDest == nil || logDest.ID <= 0 {
			return nil, errGlobalLogMissing
		}
		return logDest.InputPeer(), nil

	default:
		_ = resolver
		return nil, fmt.Errorf("%w: unsupported destination type %q", errDestinationInvalid, dest.Type)
	}
}

func deliverMaintenanceNotifications(
	parentCtx context.Context,
	msgSvc core.MessageServicer,
	ownerID int64,
	resolver core.PeerResolver,
	ulog *userlog.Service,
	event NotificationEvent,
	destinations []NotificationDestination,
	text string,
	totalBudget time.Duration,
	perDestTimeout time.Duration,
) NotificationBatchResult {
	started := time.Now()
	result := NotificationBatchResult{
		Event:        event,
		Total:        len(destinations),
		PerRecipient: make([]DestinationDeliveryResult, 0, len(destinations)),
	}
	if len(destinations) == 0 {
		return result
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if totalBudget <= 0 {
		totalBudget = maintenanceNotifyTotalBudget
	}
	if perDestTimeout <= 0 {
		perDestTimeout = maintenanceNotifyTimeout
	}

	batchCtx, batchCancel := context.WithTimeout(parentCtx, totalBudget)
	defer batchCancel()

	for i, dest := range destinations {
		if batchErr := batchCtx.Err(); batchErr != nil {
			outcome, reason := classifyBatchSkip(batchErr)
			for j := i; j < len(destinations); j++ {
				result.Skipped++
				result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
					DestinationID: destinations[j].ID,
					Type:          destinations[j].Type,
					Outcome:       outcome,
					Reason:        reason,
				})
			}
			break
		}

		if msgSvc == nil {
			result.Failed++
			result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
				DestinationID: dest.ID,
				Type:          dest.Type,
				Outcome:       DeliveryFailed,
				Reason:        "message_service_unavailable",
			})
			continue
		}

		peer, err := resolveNotificationPeer(batchCtx, dest, ownerID, resolver, ulog)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(batchCtx.Err(), context.Canceled) {
				result.Skipped++
				result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
					DestinationID: dest.ID,
					Type:          dest.Type,
					Outcome:       DeliverySkippedCancelled,
					Reason:        "context_cancelled",
				})
				continue
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(batchCtx.Err(), context.DeadlineExceeded) {
				result.Skipped++
				result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
					DestinationID: dest.ID,
					Type:          dest.Type,
					Outcome:       DeliverySkippedDeadline,
					Reason:        "total_deadline_exceeded",
				})
				continue
			}
			result.InvalidDest++
			reason := "resolve_failed"
			if errors.Is(err, errGlobalLogMissing) {
				reason = "global_log_unconfigured"
			} else if errors.Is(err, errDestinationInvalid) {
				reason = "invalid_destination"
			}
			result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
				DestinationID: dest.ID,
				Type:          dest.Type,
				Outcome:       DeliveryInvalidDest,
				Reason:        reason,
			})
			continue
		}

		destTimeout := perDestTimeout
		if deadline, ok := batchCtx.Deadline(); ok {
			rem := time.Until(deadline)
			if rem <= 0 {
				for j := i; j < len(destinations); j++ {
					result.Skipped++
					result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
						DestinationID: destinations[j].ID,
						Type:          destinations[j].Type,
						Outcome:       DeliverySkippedDeadline,
						Reason:        "total_deadline_exceeded",
					})
				}
				break
			}
			if rem < destTimeout {
				destTimeout = rem
			}
		}

		destCtx, destCancel := context.WithTimeout(batchCtx, destTimeout)
		_, sendErr := msgSvc.SendMessage(destCtx, peer, text)
		destCancel()

		if sendErr != nil {
			if errors.Is(parentCtx.Err(), context.Canceled) || (errors.Is(batchCtx.Err(), context.Canceled) && errors.Is(sendErr, context.Canceled)) {
				result.Skipped++
				result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
					DestinationID: dest.ID,
					Type:          dest.Type,
					Outcome:       DeliverySkippedCancelled,
					Reason:        "context_cancelled",
				})
				continue
			}
			result.Failed++
			reason := "send_failed"
			if errors.Is(sendErr, context.DeadlineExceeded) {
				reason = "timeout"
			} else if errors.Is(sendErr, context.Canceled) {
				reason = "cancelled"
			}
			result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
				DestinationID: dest.ID,
				Type:          dest.Type,
				Outcome:       DeliveryFailed,
				Reason:        reason,
			})
			continue
		}

		result.Sent++
		result.PerRecipient = append(result.PerRecipient, DestinationDeliveryResult{
			DestinationID: dest.ID,
			Type:          dest.Type,
			Outcome:       DeliverySent,
		})
	}

	result.Duration = time.Since(started)
	return result
}

func classifyBatchSkip(batchErr error) (DeliveryOutcomeKind, string) {
	if errors.Is(batchErr, context.Canceled) {
		return DeliverySkippedCancelled, "context_cancelled"
	}
	return DeliverySkippedDeadline, "total_deadline_exceeded"
}
