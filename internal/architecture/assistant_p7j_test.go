package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP7JGroupContextAndTopicFencesRemainCanonical(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "core", "context.go"): {
			"ReplyPeer        PeerRef",
			"replied message belongs to another chat",
			"replied message crosses forum topics",
			"func (c *Context) RepliedToSelf()",
			"func (c *Context) AddressedToSelf()",
		},
		filepath.Join(root, "internal", "core", "context_messages.go"): {
			"telegram transport cannot preserve forum topic",
		},
		filepath.Join(root, "internal", "core", "context_media.go"): {
			"telegram transport cannot preserve forum topic",
		},
		filepath.Join(root, "internal", "core", "group_context.go"): {
			"func GroupOrderingKey(chatID int64, topicID int) string",
			"chat:%d:topic:%d",
		},
		filepath.Join(root, "internal", "assistant", "client", "updates.go"): {
			"Media:            core.ExtractMediaFromTG(message.Media)",
			"ReplyPeer:        assistantReplyPeer(message, entities)",
			"MentionedSelf:    assistantMentionsSelf(mentions, selfID, selfUsername)",
			"core.GroupOrderingKey(chatID, assistantTopicID(message))",
		},
		filepath.Join(root, "internal", "assistant", "command", "router.go"): {
			"orderingKey = core.GroupOrderingKey(coreCtx.Chat.ID, coreCtx.TopicID())",
			"Media:            messageContext.Media",
			"MentionedSelf:    messageContext.MentionedSelf",
			"saved response transport cannot preserve forum topic",
		},
		filepath.Join(root, "internal", "assistant", "command", "servicer.go"): {
			"assistant interaction cannot preserve forum topic",
		},
		filepath.Join(root, "internal", "assistant", "interaction", "message.go"): {
			"reply.TopMsgID = send.TopicID",
			"func (c *ClientInteraction) SendMediaContext(",
		},
		filepath.Join(root, "plugins", "filters", "filters.go"): {
			"core.GroupOrderingKey(chatID, topicID)",
			"core.MessageSendContext{ReplyToID: message.ID, TopicID: message.TopicID}",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Errorf("P7-J invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP7JDoesNotIntroduceTopicWorkersOrPolling(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "core", "group_context.go"),
		filepath.Join("internal", "assistant", "client", "p7j_context_test.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{"time.NewTicker", "time.Tick(", "time.AfterFunc("} {
			if strings.Contains(source, forbidden) {
				t.Errorf("P7-J must remain occurrence-driven; %s contains %q", rel, forbidden)
			}
		}
	}
}
