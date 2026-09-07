package testing

import (
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// FixtureUser returns a mock tg.User with standard test attributes.
func FixtureUser(id int64, username string) *tg.User {
	return &tg.User{
		ID:         id,
		AccessHash: 11223344,
		FirstName:  "Test",
		LastName:   "User",
		Username:   username,
	}
}

// FixtureMessageTarget creates a standard MessageTarget for tests.
func FixtureMessageTarget(userID int64, msgID int) interaction.MessageTarget {
	return interaction.NewMessageTarget(&tg.InputPeerUser{
		UserID:     userID,
		AccessHash: 11223344,
	}, msgID, userID, 9999)
}

// FixtureInlineTarget creates a standard InlineTarget for tests.
func FixtureInlineTarget(queryID int64) interaction.InlineTarget {
	return interaction.NewInlineTarget(queryID, &tg.InputBotInlineMessageID{
		DCID:       1,
		ID:         55555,
		AccessHash: 66666,
	}, 9999)
}
