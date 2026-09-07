package ui

// Contextual action names
const (
	ActionUserMute  = "mute"
	ActionUserBan   = "ban"
	ActionUserWarn  = "warn"
	ActionUserNotes = "notes"

	ActionChatSettings   = "settings"
	ActionChatMembers    = "members"
	ActionChatModeration = "moderation"
	ActionChatInfo       = "info"

	ActionMsgPin      = "pin"
	ActionMsgDelete   = "delete"
	ActionMsgReply    = "reply"
	ActionMsgDownload = "download"
)

// BuildUserActionBar generates standard contextual moderation and info buttons for a user entity.
func BuildUserActionBar(userID int64, username string, makeActionData func(action string) []byte) []ButtonRow {
	return []ButtonRow{
		{
			NewCallbackButton("🔇 Mute", makeActionData(ActionUserMute)),
			NewCallbackButton("🔨 Ban", makeActionData(ActionUserBan)),
		},
		{
			NewCallbackButton("⚠️ Warn", makeActionData(ActionUserWarn)),
			NewCallbackButton("📋 Notes", makeActionData(ActionUserNotes)),
		},
	}
}

// BuildChatActionBar generates standard contextual management buttons for a chat entity.
func BuildChatActionBar(chatID int64, makeActionData func(action string) []byte) []ButtonRow {
	return []ButtonRow{
		{
			NewCallbackButton("⚙️ Settings", makeActionData(ActionChatSettings)),
			NewCallbackButton("👥 Members", makeActionData(ActionChatMembers)),
		},
		{
			NewCallbackButton("🛡 Moderation", makeActionData(ActionChatModeration)),
			NewCallbackButton("📊 Info", makeActionData(ActionChatInfo)),
		},
	}
}

// BuildMessageActionBar generates standard contextual buttons for a message entity.
func BuildMessageActionBar(msgID int, makeActionData func(action string) []byte) []ButtonRow {
	return []ButtonRow{
		{
			NewCallbackButton("📌 Pin", makeActionData(ActionMsgPin)),
			NewCallbackButton("🗑 Delete", makeActionData(ActionMsgDelete)),
		},
		{
			NewCallbackButton("↩ Reply", makeActionData(ActionMsgReply)),
			NewCallbackButton("📥 Download", makeActionData(ActionMsgDownload)),
		},
	}
}

// BuildLoadingButton creates a button indicating an ongoing operation (e.g. [ ⏳ Processing... ]).
func BuildLoadingButton(text string) Button {
	if text == "" {
		text = "⏳ Processing..."
	}
	return NewCallbackButton(text, NoopData)
}

// BuildRetryRow creates a transient error recovery button bar:
// [ 🔄 Retry ] [ ℹ️ Details ] [ ❌ Cancel ]
func BuildRetryRow(retryData, detailsData, cancelData []byte) ButtonRow {
	var row ButtonRow
	if len(retryData) > 0 {
		row = append(row, NewCallbackButton("🔄 Retry", retryData))
	}
	if len(detailsData) > 0 {
		row = append(row, NewCallbackButton("ℹ️ Details", detailsData))
	}
	if len(cancelData) > 0 {
		row = append(row, NewCallbackButton("❌ Cancel", cancelData))
	}
	return row
}
