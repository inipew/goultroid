package telegram

import "github.com/inipew/goultroid/internal/core"

// Keep one concrete Telegram transport while exposing capability-sized
// contracts to consumers. These assertions intentionally live beside Service so
// an implementation drift fails at compile time.
var (
	_ core.MessageServicer         = (*Service)(nil)
	_ core.AdminServicer           = (*Service)(nil)
	_ core.MediaServicer           = (*Service)(nil)
	_ core.PeerServicer            = (*Service)(nil)
	_ core.ProfileServicer         = (*Service)(nil)
	_ core.CommandTelegramServicer = (*Service)(nil)

	_ core.ContextualMessageServicer = (*Service)(nil)
	_ core.ContextualMediaServicer   = (*Service)(nil)
)
