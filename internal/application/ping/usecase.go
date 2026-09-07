package ping

import (
	"fmt"
	"time"
)

// Result captures the latency metric for a ping operation.
type Result struct {
	Latency time.Duration
}

// UseCase executes a latency probe across surfaces (§8, §17 bug16_1).
type UseCase struct{}

// NewUseCase creates an initialized ping UseCase.
func NewUseCase() *UseCase {
	return &UseCase{}
}

// Execute performs latency measurement with an optional probe closure.
func (u *UseCase) Execute(probe func() error) (Result, error) {
	start := time.Now()
	if probe != nil {
		if err := probe(); err != nil {
			return Result{Latency: time.Since(start)}, err
		}
	}
	return Result{Latency: time.Since(start)}, nil
}

// FormatResult produces the uniform Telegram HTML response across Userbot and Assistant.
func FormatResult(latency time.Duration) string {
	return fmt.Sprintf("🏓 <b>Pong!</b>\n⚡ <b>Latency:</b> <code>%d ms</code>", latency.Milliseconds())
}
