package core

import (
	"context"

	"github.com/gotd/td/tg"
)

// MessageServicer is the command-context transport boundary for ordinary
// Telegram message operations. Consumers that only need messaging should
// depend on this interface instead of the broad compatibility service.
type MessageServicer interface {
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error)
	SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error)
	EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error
	EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error
	EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error
	DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error
	React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error
	GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error)
	PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error
	UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error
	ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error
	PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error)
}

// AdminServicer is the command-context boundary for moderation and chat-admin
// mutations.
type AdminServicer interface {
	BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error
	DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error
}

// MediaServicer is the command-context boundary for file transfer and media
// delivery.
type MediaServicer interface {
	DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error
	SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error)
}

// PeerServicer is the command-context boundary for peer lookup and peer-level
// user state.
type PeerServicer interface {
	GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error)
	ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error)
	GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error)
	BlockUser(ctx context.Context, peer tg.InputPeerClass) error
	UnblockUser(ctx context.Context, peer tg.InputPeerClass) error
}

// ProfileServicer is the boundary for self-profile, dialog, and contact
// operations.
type ProfileServicer interface {
	UpdateProfile(ctx context.Context, firstName, lastName, about *string) error
	UploadProfilePhoto(ctx context.Context, filePath string) error
	DeleteProfilePhotos(ctx context.Context, limit int) (int, error)
	GetDialogs(ctx context.Context, limit int) ([]*Chat, error)
	GetContacts(ctx context.Context) ([]*User, error)
}

// CommandTelegramServicer is the aggregate required by the legacy command
// Context. It intentionally excludes callback answers, inline-query answers,
// inline-message editing, and bot-origin tracking. Context migration should
// target the individual capability interfaces rather than this aggregate.
type CommandTelegramServicer interface {
	MessageServicer
	AdminServicer
	MediaServicer
	PeerServicer
	ProfileServicer
}

// ContextualMessageServicer is the optional extension for preserving Telegram
// reply/topic coordinates when sending text or markup.
type ContextualMessageServicer interface {
	SendMessageContext(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass, MessageSendContext) (*tg.Message, error)
}

// ContextualMediaServicer is the optional extension for preserving Telegram
// reply/topic coordinates when sending media.
type ContextualMediaServicer interface {
	SendMediaContext(context.Context, tg.InputPeerClass, string, string, string, MessageSendContext) (*tg.Message, error)
}

// GroupRuleTransport is the canonical Assistant group-rule action boundary.
// Blacklist only consumes deletion while filters consume text/media delivery;
// sharing this small union keeps both rule sources compatible without restoring
// the legacy TelegramServicer aggregate.
type GroupRuleTransport interface {
	SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error)
	SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error)
	DeleteMessage(context.Context, tg.InputPeerClass, []int) error
}

// The broad compatibility service must continue to cover every command-context
// capability during M1. M2 will migrate callers away from the broad boundary.
var (
	_ MessageServicer         = (TelegramServicer)(nil)
	_ AdminServicer           = (TelegramServicer)(nil)
	_ MediaServicer           = (TelegramServicer)(nil)
	_ PeerServicer            = (TelegramServicer)(nil)
	_ ProfileServicer         = (TelegramServicer)(nil)
	_ CommandTelegramServicer = (TelegramServicer)(nil)

	_ ContextualMessageServicer = (ContextualTelegramServicer)(nil)
	_ ContextualMediaServicer   = (ContextualTelegramServicer)(nil)
)
