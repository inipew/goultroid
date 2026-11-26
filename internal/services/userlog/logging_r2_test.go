package userlog

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

type r2MockRepo struct {
	settings map[string]string
}

func (r *r2MockRepo) InitSchema(context.Context) error { return nil }

func (r *r2MockRepo) GetUserLogSetting(_ context.Context, key string) (string, error) {
	return r.settings[key], nil
}

func (r *r2MockRepo) SetUserLogSetting(_ context.Context, key, val string) error {
	r.settings[key] = val
	return nil
}

type r2MockSender struct {
	sent []string
}

func (s *r2MockSender) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.sent = append(s.sent, text)
	return &tg.Message{ID: len(s.sent)}, nil
}

func TestUserLog_RedactsSecretsInPMMentionAndActions(t *testing.T) {
	repo := &r2MockRepo{
		settings: map[string]string{
			SettingLogDestination: `{"id":12345,"type":"chat"}`,
		},
	}
	sender := &r2MockSender{}
	svc := NewService(repo, sender, zap.NewNop())

	ctx := context.Background()
	if err := svc.LogPM(ctx, "Alice", 10, `my token=secret123 and https://user:pw@example.com/p?api_key=abc`); err != nil {
		t.Fatalf("LogPM: %v", err)
	}
	if err := svc.LogMention(ctx, "Group", "Bob", 20, `Bearer my-jwt-token password="my secret"`); err != nil {
		t.Fatalf("LogMention: %v", err)
	}
	if err := svc.LogActionDetailed(ctx, "ban", 30, "Eve", 1, "Group", `x-api-key=leak1`, false, `https://example.com/?token=leak2`); err != nil {
		t.Fatalf("LogActionDetailed: %v", err)
	}

	if len(sender.sent) != 3 {
		t.Fatalf("expected 3 messages sent, got %d", len(sender.sent))
	}
	joined := strings.Join(sender.sent, "\n")
	for _, bad := range []string{"secret123", "pw", "abc", "my-jwt-token", "my secret", "leak1", "leak2"} {
		if strings.Contains(joined, bad) {
			t.Errorf("UserLog output leaked %q in:\n%s", bad, joined)
		}
	}
}

func TestUserLog_PreservesNormalProseAndEscapesHTML(t *testing.T) {
	repo := &r2MockRepo{
		settings: map[string]string{
			SettingLogDestination: `{"id":12345,"type":"chat"}`,
		},
	}
	sender := &r2MockSender{}
	svc := NewService(repo, sender, zap.NewNop())

	prose := "please check the token guide & password policy <script>alert(1)</script> for key details"
	if err := svc.LogPM(context.Background(), "Alice", 10, prose); err != nil {
		t.Fatalf("LogPM: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sender.sent))
	}
	out := sender.sent[0]
	for _, keep := range []string{"token guide", "password policy", "key details"} {
		if !strings.Contains(out, keep) {
			t.Errorf("normal prose %q was falsely redacted in %q", keep, out)
		}
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("raw HTML tag was not escaped in %q", out)
	}
}
