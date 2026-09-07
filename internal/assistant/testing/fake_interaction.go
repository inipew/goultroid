package testing

import (
	"context"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
)

// FakeInteraction is a thread-safe mock implementation of MessageInteraction.
type FakeInteraction struct {
	mu             sync.RWMutex
	AnswerCalls    int
	LastAnswerText string
	LastAlert      bool
	LastEditedText string
	Deleted        bool
	SentMessages   []string
	EditCalls      int

	// Error injection
	AnswerErr error
	EditErr   error
	DeleteErr error
}

var _ interaction.MessageInteraction = (*FakeInteraction)(nil)

// NewFakeInteraction creates an initialized FakeInteraction mock.
func NewFakeInteraction() *FakeInteraction {
	return &FakeInteraction{
		SentMessages: make([]string, 0),
	}
}

func (f *FakeInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AnswerCalls++
	f.LastAnswerText = text
	f.LastAlert = alert
	return f.AnswerErr
}

func (f *FakeInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EditCalls++
	f.LastEditedText = text
	return f.EditErr
}

func (f *FakeInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.EditErr
}

func (f *FakeInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deleted = true
	return f.DeleteErr
}

func (f *FakeInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}

func (f *FakeInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SentMessages = append(f.SentMessages, text)
	return &tg.Message{ID: 100}, nil
}

// FakeInlineInteraction is a mock implementation of InlineInteraction.
type FakeInlineInteraction struct {
	mu             sync.RWMutex
	AnswerCalls    int
	LastAnswerText string
	LastAlert      bool
	LastEditedText string
	EditErr        error
	AnswerErr      error
}

var _ interaction.InlineInteraction = (*FakeInlineInteraction)(nil)

func NewFakeInlineInteraction() *FakeInlineInteraction {
	return &FakeInlineInteraction{}
}

func (f *FakeInlineInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AnswerCalls++
	f.LastAnswerText = text
	f.LastAlert = alert
	return f.AnswerErr
}

func (f *FakeInlineInteraction) Edit(ctx context.Context, target interaction.InlineTarget, text string, markup tg.ReplyMarkupClass) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LastEditedText = text
	return f.EditErr
}
