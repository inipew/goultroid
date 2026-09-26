package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0CEditOrReplyUsesPreflightRoutingAndFailClosedEditDelivery(t *testing.T) {
	root := repositoryRoot(t)

	contextPath := filepath.Join(root, "internal", "core", "context.go")
	contextRaw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contextRaw), "Falls back to Reply if editing fails") {
		t.Fatal("Context.EditOrReply still documents unsafe fallback after an attempted edit")
	}

	messagesPath := filepath.Join(root, "internal", "core", "context_messages.go")
	messagesRaw, err := os.ReadFile(messagesPath)
	if err != nil {
		t.Fatal(err)
	}
	messages := string(messagesRaw)
	for _, required := range []string{
		"editableResponseAnchor(c)",
		"m.editMessageID(text, msgID)",
		"MessageEditStagePostCommit",
		"messageEditFailure(MessageEditStagePostCommit, true, err)",
	} {
		if !strings.Contains(messages, required) {
			t.Fatalf("EditOrReply contract missing %q", required)
		}
	}

	contractPath := filepath.Join(root, "internal", "core", "message_edit.go")
	contractRaw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	contract := string(contractRaw)
	for _, required := range []string{
		"ErrEditDeliveryUnconfirmed",
		"type MessageEditFailure struct",
		"MayHaveCommitted bool",
		"MessageEditFallbackSafe",
		"MessageEditStagePreflight",
		"MessageEditStageSend",
	} {
		if !strings.Contains(contract, required) {
			t.Fatalf("stage-aware edit contract missing %q", required)
		}
	}

	responsePath := filepath.Join(root, "internal", "core", "response.go")
	responseRaw, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(responseRaw), "c.Message.IsOutgoing") {
		t.Fatal("SemanticResponse reintroduced an outgoing edit special-case instead of using EditOrReply")
	}
}
