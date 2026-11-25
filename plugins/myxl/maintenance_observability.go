package myxl

import (
	"context"
	"fmt"
	"html"
	"strings"
	"sync/atomic"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

const maintenanceNotifyTimeout = 5 * time.Second

// MaintenanceDiagnostics exposes bounded, low-cardinality operational counters
// for MyXL token maintenance without any per-account or per-run labels.
type MaintenanceDiagnostics struct {
	RunsTotal              int64 `json:"runs_total"`
	RunsSucceeded          int64 `json:"runs_succeeded"`
	RunsPartialFailure     int64 `json:"runs_partial_failure"`
	RunsFailed             int64 `json:"runs_failed"`
	RunsCancelled          int64 `json:"runs_cancelled"`
	RunsNoOp               int64 `json:"runs_no_op"`
	AccountsExamined       int64 `json:"accounts_examined"`
	AccountsRefreshed      int64 `json:"accounts_refreshed"`
	AccountsFailed         int64 `json:"accounts_failed"`
	AccountsSkipped        int64 `json:"accounts_skipped"`
	NotificationsAttempted int64 `json:"notifications_attempted"`
	NotificationsSucceeded int64 `json:"notifications_succeeded"`
	NotificationsFailed    int64 `json:"notifications_failed"`
	NotificationsSkipped   int64 `json:"notifications_skipped"`
	NotificationsInvalid   int64 `json:"notifications_invalid"`
}

type maintenanceCounters struct {
	runsTotal              atomic.Int64
	runsSucceeded          atomic.Int64
	runsPartialFailure     atomic.Int64
	runsFailed             atomic.Int64
	runsCancelled          atomic.Int64
	runsNoOp               atomic.Int64
	accountsExamined       atomic.Int64
	accountsRefreshed      atomic.Int64
	accountsFailed         atomic.Int64
	accountsSkipped        atomic.Int64
	notificationsAttempted atomic.Int64
	notificationsSucceeded atomic.Int64
	notificationsFailed    atomic.Int64
	notificationsSkipped   atomic.Int64
	notificationsInvalid   atomic.Int64
}

// SetLogger configures the structured zap logger for MyXL lifecycle events.
func (p *Plugin) SetLogger(l *zap.Logger) {
	if p == nil {
		return
	}
	p.maintMu.Lock()
	p.logger = l
	p.maintMu.Unlock()
}

func (p *Plugin) getLogger() *zap.Logger {
	if p == nil {
		return zap.NewNop()
	}
	p.maintMu.RLock()
	l := p.logger
	p.maintMu.RUnlock()
	if l == nil {
		return zap.NewNop()
	}
	return l
}

// SetOwnerNotifier configures best-effort owner Telegram delivery using the
// runtime's managed MessageServicer.
func (p *Plugin) SetOwnerNotifier(ownerID int64, msgSvc func() core.MessageServicer) {
	if p == nil {
		return
	}
	p.maintMu.Lock()
	p.ownerID = ownerID
	p.msgSvcFunc = msgSvc
	p.maintMu.Unlock()
}

// MaintenanceDiagnostics returns a point-in-time snapshot of maintenance counters.
func (p *Plugin) MaintenanceDiagnostics() MaintenanceDiagnostics {
	if p == nil {
		return MaintenanceDiagnostics{}
	}
	return MaintenanceDiagnostics{
		RunsTotal:              p.maintDiag.runsTotal.Load(),
		RunsSucceeded:          p.maintDiag.runsSucceeded.Load(),
		RunsPartialFailure:     p.maintDiag.runsPartialFailure.Load(),
		RunsFailed:             p.maintDiag.runsFailed.Load(),
		RunsCancelled:          p.maintDiag.runsCancelled.Load(),
		RunsNoOp:               p.maintDiag.runsNoOp.Load(),
		AccountsExamined:       p.maintDiag.accountsExamined.Load(),
		AccountsRefreshed:      p.maintDiag.accountsRefreshed.Load(),
		AccountsFailed:         p.maintDiag.accountsFailed.Load(),
		AccountsSkipped:        p.maintDiag.accountsSkipped.Load(),
		NotificationsAttempted: p.maintDiag.notificationsAttempted.Load(),
		NotificationsSucceeded: p.maintDiag.notificationsSucceeded.Load(),
		NotificationsFailed:    p.maintDiag.notificationsFailed.Load(),
		NotificationsSkipped:   p.maintDiag.notificationsSkipped.Load(),
		NotificationsInvalid:   p.maintDiag.notificationsInvalid.Load(),
	}
}

func (p *Plugin) recordMaintenanceDiagnostics(summary *MaintenanceRunSummary) {
	if p == nil || summary == nil {
		return
	}
	p.maintDiag.runsTotal.Add(1)
	p.maintDiag.accountsExamined.Add(int64(summary.TotalAccounts))
	p.maintDiag.accountsRefreshed.Add(int64(summary.RefreshedCount))
	p.maintDiag.accountsFailed.Add(int64(summary.FailedCount))
	p.maintDiag.accountsSkipped.Add(int64(summary.SkippedCount + summary.AlreadyFreshCount))

	switch summary.Status {
	case RunStatusCancelled:
		p.maintDiag.runsCancelled.Add(1)
	case RunStatusFailed:
		p.maintDiag.runsFailed.Add(1)
	case RunStatusPartialFailure:
		p.maintDiag.runsPartialFailure.Add(1)
	case RunStatusCompleted:
		if summary.RefreshedCount == 0 {
			p.maintDiag.runsNoOp.Add(1)
		} else {
			p.maintDiag.runsSucceeded.Add(1)
		}
	}
}

func primaryErrorClass(outcomes []AccountMaintenanceOutcome) string {
	for _, o := range outcomes {
		switch o.Status {
		case StatusAuthenticationRejected, StatusRateLimited, StatusNetworkError, StatusPersistenceError, StatusMissingRefreshToken:
			return string(o.Status)
		}
	}
	return "none"
}

func (p *Plugin) logMaintenanceStarted(triggerSource string) {
	p.getLogger().Info("myxl token maintenance started",
		zap.String("plugin", "myxl"),
		zap.String("job_id", maintenanceJobID),
		zap.String("trigger_source", triggerSource),
	)
}

func (p *Plugin) logMaintenanceFinished(summary *MaintenanceRunSummary, outcomes []AccountMaintenanceOutcome, persistErr error) {
	logger := p.getLogger()
	if summary == nil {
		logger.Error("myxl token maintenance failed before summary",
			zap.String("plugin", "myxl"),
			zap.String("job_id", maintenanceJobID),
			zap.String("outcome", "failed"),
			zap.String("error_class", "internal_error"),
		)
		return
	}

	outcome := string(summary.Status)
	if summary.Status == RunStatusCompleted && summary.RefreshedCount == 0 {
		outcome = "no_op"
	}
	errClass := primaryErrorClass(outcomes)
	if persistErr != nil {
		errClass = string(StatusPersistenceError)
	}

	fields := []zap.Field{
		zap.String("plugin", "myxl"),
		zap.String("job_id", maintenanceJobID),
		zap.Int64("run_id", summary.ID),
		zap.Duration("duration", summary.Duration),
		zap.Int("account_count", summary.TotalAccounts),
		zap.Int("processed_count", summary.RefreshedCount+summary.FailedCount),
		zap.Int("success_count", summary.RefreshedCount),
		zap.Int("failure_count", summary.FailedCount),
		zap.Int("skipped_count", summary.SkippedCount+summary.AlreadyFreshCount),
		zap.String("outcome", outcome),
		zap.String("error_class", errClass),
	}

	switch {
	case persistErr != nil || summary.Status == RunStatusFailed:
		logger.Error("myxl token maintenance failed", fields...)
	case summary.Status == RunStatusCancelled:
		logger.Warn("myxl token maintenance cancelled", fields...)
	case summary.Status == RunStatusPartialFailure:
		logger.Warn("myxl token maintenance partially failed", fields...)
	default:
		logger.Info("myxl token maintenance completed", fields...)
	}
}

func shouldNotifyOwner(cfg TokenMaintenanceConfig, summary *MaintenanceRunSummary) bool {
	if summary == nil {
		return false
	}
	switch summary.Status {
	case RunStatusCompleted:
		return cfg.NotifyOnCompletion && summary.RefreshedCount > 0
	case RunStatusPartialFailure, RunStatusFailed:
		return cfg.NotifyOnFailure
	default:
		return false
	}
}

func (p *Plugin) maybeNotifyOwner(cfg TokenMaintenanceConfig, summary *MaintenanceRunSummary, outcomes []AccountMaintenanceOutcome) {
	p.maybeNotifyDestinations(context.Background(), cfg, summary, outcomes)
}

func (p *Plugin) maybeNotifyDestinations(parentCtx context.Context, cfg TokenMaintenanceConfig, summary *MaintenanceRunSummary, outcomes []AccountMaintenanceOutcome) {
	if p == nil || summary == nil {
		return
	}
	event, enabled := classifyMaintenanceNotificationEvent(cfg, summary)
	if !enabled {
		return
	}
	destinations := effectiveNotificationDestinations(cfg)
	disabledCount := 0
	if len(cfg.Destinations) > 0 {
		disabledCount = len(cfg.Destinations) - len(destinations)
	}
	if len(destinations) == 0 {
		if disabledCount > 0 {
			p.maintDiag.notificationsSkipped.Add(int64(disabledCount))
		}
		p.getLogger().Info("myxl token maintenance notification skipped",
			zap.String("plugin", "myxl"),
			zap.String("job_id", maintenanceJobID),
			zap.Int64("run_id", summary.ID),
			zap.String("event_type", string(event)),
			zap.Bool("fallback_owner", false),
			zap.Int("destination_count", len(cfg.Destinations)),
			zap.Int("skipped_count", disabledCount),
			zap.String("notification_status", "skipped"),
		)
		return
	}

	p.maintMu.RLock()
	ownerID := p.ownerID
	msgSvcFn := p.msgSvcFunc
	resolver := p.peerResolver
	ulog := p.userlogSvc
	p.maintMu.RUnlock()

	var msgSvc core.MessageServicer
	if msgSvcFn != nil {
		msgSvc = msgSvcFn()
	}

	text := formatMaintenanceNotificationHTML(summary, outcomes)
	batchRes := deliverMaintenanceNotifications(
		parentCtx,
		msgSvc,
		ownerID,
		resolver,
		ulog,
		event,
		destinations,
		text,
		maintenanceNotifyTotalBudget,
		maintenanceNotifyTimeout,
	)

	attempted := int64(batchRes.Sent + batchRes.Failed + batchRes.InvalidDest)
	if attempted > 0 {
		p.maintDiag.notificationsAttempted.Add(attempted)
	}
	if batchRes.Sent > 0 {
		p.maintDiag.notificationsSucceeded.Add(int64(batchRes.Sent))
	}
	if batchRes.Failed > 0 {
		p.maintDiag.notificationsFailed.Add(int64(batchRes.Failed))
	}
	if batchRes.InvalidDest > 0 {
		p.maintDiag.notificationsInvalid.Add(int64(batchRes.InvalidDest))
	}
	totalSkipped := batchRes.Skipped + disabledCount
	if totalSkipped > 0 {
		p.maintDiag.notificationsSkipped.Add(int64(totalSkipped))
	}

	fields := []zap.Field{
		zap.String("plugin", "myxl"),
		zap.String("job_id", maintenanceJobID),
		zap.Int64("run_id", summary.ID),
		zap.String("event_type", string(event)),
		zap.Bool("fallback_owner", len(cfg.Destinations) == 0),
		zap.Int("destination_count", batchRes.Total+disabledCount),
		zap.Int("sent_count", batchRes.Sent),
		zap.Int("failure_count", batchRes.Failed),
		zap.Int("invalid_count", batchRes.InvalidDest),
		zap.Int("skipped_count", totalSkipped),
		zap.Duration("duration", batchRes.Duration),
	}
	switch {
	case batchRes.Sent > 0 && (batchRes.Failed > 0 || batchRes.InvalidDest > 0):
		fields = append(fields, zap.String("notification_status", "partial_failure"))
		p.getLogger().Warn("myxl token maintenance notification partially failed", fields...)
	case batchRes.Failed > 0 || batchRes.InvalidDest > 0:
		fields = append(fields, zap.String("notification_status", "failed"))
		p.getLogger().Warn("myxl token maintenance notification failed", fields...)
	case batchRes.Sent == 0 && totalSkipped > 0:
		fields = append(fields, zap.String("notification_status", "skipped"))
		p.getLogger().Info("myxl token maintenance notification skipped", fields...)
	default:
		fields = append(fields, zap.String("notification_status", "sent"))
		p.getLogger().Info("myxl token maintenance notification sent", fields...)
	}
}

func formatMaintenanceNotificationHTML(summary *MaintenanceRunSummary, outcomes []AccountMaintenanceOutcome) string {
	var b strings.Builder
	title := "MyXL Token Maintenance"
	switch summary.Status {
	case RunStatusPartialFailure:
		title = "MyXL Token Maintenance — Partial Failure"
	case RunStatusFailed:
		title = "MyXL Token Maintenance — Failed"
	case RunStatusCancelled:
		title = "MyXL Token Maintenance — Cancelled"
	}
	fmt.Fprintf(&b, "🕒 <b>%s</b>\n\n", html.EscapeString(title))
	fmt.Fprintf(&b, "• <b>Status:</b> <code>%s</code> (%s)\n", html.EscapeString(string(summary.Status)), html.EscapeString(summary.TriggerSource))
	fmt.Fprintf(&b, "• <b>Durasi:</b> %s\n", summary.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "• <b>Ringkasan:</b> total=%d, refreshed=%d, fresh=%d, skipped=%d, failed=%d",
		summary.TotalAccounts, summary.RefreshedCount, summary.AlreadyFreshCount, summary.SkippedCount, summary.FailedCount)
	if errClass := primaryErrorClass(outcomes); errClass != "none" {
		fmt.Fprintf(&b, "\n• <b>Kategori Error:</b> <code>%s</code>", html.EscapeString(errClass))
	}
	return b.String()
}
