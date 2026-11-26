package telegram

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

func TestDispatcher_SavedMessagesOutgoingCommandDispatch(t *testing.T) {
	const selfID int64 = 589287392

	router := core.NewRouter(".")
	perms := core.NewPermissions(selfID, nil)

	var (
		mu       sync.Mutex
		invoked  []string
		peerSelf []bool
		chatIDs  []int64
	)

	if err := router.Register(core.Command{
		Name:        "ytcookies",
		Permission:  core.PermissionOwner,
		PrivateOnly: true,
		Surfaces:    execution.SurfaceUserbot,
		Handler: func(ctx *core.Context) error {
			mu.Lock()
			defer mu.Unlock()
			sub := ""
			if len(ctx.Args) > 0 {
				sub = ctx.Args[0]
			}
			invoked = append(invoked, sub)
			_, isSelf := ctx.PeerID.(*tg.InputPeerSelf)
			peerSelf = append(peerSelf, isSelf)
			chatIDs = append(chatIDs, ctx.ChatID())
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(router, perms, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	d.SetSelfID(selfID)

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			selfID: {ID: selfID, Self: true},
		},
	}

	// 1. Outgoing .ytcookies reply, .ytcookies set, .ytcookies status, .ytcookies delete in Saved Messages.
	for i, text := range []string{".ytcookies", ".ytcookies set", ".ytcookies status", ".ytcookies delete"} {
		err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:     100 + i,
				Out:    true,
				PeerID: &tg.PeerUser{UserID: selfID},
				ReplyTo: &tg.MessageReplyHeader{
					ReplyToMsgID: 99,
				},
				Message: text,
			},
		})
		if err != nil {
			t.Fatalf("OnNewMessage(%q) error = %v", text, err)
		}
	}

	// 2. Ordinary outgoing non-command message in Saved Messages must not invoke command.
	if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      200,
			Out:     true,
			PeerID:  &tg.PeerUser{UserID: selfID},
			Message: "just a normal saved note",
		},
	}); err != nil {
		t.Fatalf("OnNewMessage(normal text) error = %v", err)
	}

	// Wait briefly for TaskEngine async command execution.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(invoked)
		mu.Unlock()
		if n == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(invoked) != 4 {
		t.Fatalf("invoked = %v, want 4 commands", invoked)
	}
	gotSubs := append([]string(nil), invoked...)
	slices.Sort(gotSubs)
	wantSubs := []string{"", "delete", "set", "status"}
	for i, want := range wantSubs {
		if gotSubs[i] != want {
			t.Fatalf("sorted invoked[%d] = %q, want %q (all=%v)", i, gotSubs[i], want, invoked)
		}
		if !peerSelf[i] {
			t.Fatalf("peerSelf[%d] = false, want InputPeerSelf", i)
		}
		if chatIDs[i] != selfID {
			t.Fatalf("chatIDs[%d] = %d, want %d", i, chatIDs[i], selfID)
		}
	}
}

func TestDispatcher_SavedMessagesRuntimeOutFalseFromIDNil(t *testing.T) {
	const selfID int64 = 589287392
	const otherUserID int64 = 999888777

	router := core.NewRouter(".")
	perms := core.NewPermissions(selfID, nil)

	var (
		mu      sync.Mutex
		invoked []string
		senders []int64
	)

	if err := router.Register(core.Command{
		Name:        "ytcookies",
		Permission:  core.PermissionOwner,
		PrivateOnly: true,
		Surfaces:    execution.SurfaceUserbot,
		Handler: func(ctx *core.Context) error {
			mu.Lock()
			defer mu.Unlock()
			sub := ""
			if len(ctx.Args) > 0 {
				sub = ctx.Args[0]
			}
			invoked = append(invoked, sub)
			senders = append(senders, ctx.SenderID())
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(router, perms, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	d.SetSelfID(selfID)

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			selfID:      {ID: selfID, Self: true},
			otherUserID: {ID: otherUserID},
		},
	}

	// 1. Exact runtime shape from DL-F3 log: Out: false, PeerUser{selfID}, FromID: nil.
	for i, text := range []string{".ytcookies", ".ytcookies set", ".ytcookies status", ".ytcookies delete"} {
		if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:     300 + i,
				Out:    false,
				PeerID: &tg.PeerUser{UserID: selfID},
				FromID: nil,
				ReplyTo: &tg.MessageReplyHeader{
					ReplyToMsgID: 299,
				},
				Message: text,
			},
		}); err != nil {
			t.Fatalf("OnNewMessage(%q) error = %v", text, err)
		}
	}

	// 2. Non-owner private DM (Out: false, PeerUser{otherUserID}, FromID: nil) must be rejected.
	if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      400,
			Out:     false,
			PeerID:  &tg.PeerUser{UserID: otherUserID},
			FromID:  nil,
			Message: ".ytcookies status",
		},
	}); err != nil {
		t.Fatalf("OnNewMessage(other user DM) error = %v", err)
	}

	// 3. Conflicting FromID in Saved Messages (Out: false, PeerUser{selfID}, FromID: PeerUser{otherUserID}) must be dropped.
	if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      401,
			Out:     false,
			PeerID:  &tg.PeerUser{UserID: selfID},
			FromID:  &tg.PeerUser{UserID: otherUserID},
			Message: ".ytcookies status",
		},
	}); err != nil {
		t.Fatalf("OnNewMessage(conflicting FromID) error = %v", err)
	}

	// 4. Ordinary non-command message with Out: false, FromID: nil must not invoke command.
	if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      402,
			Out:     false,
			PeerID:  &tg.PeerUser{UserID: selfID},
			FromID:  nil,
			Message: "hello self",
		},
	}); err != nil {
		t.Fatalf("OnNewMessage(normal text) error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(invoked)
		mu.Unlock()
		if n == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(invoked) != 4 {
		t.Fatalf("invoked = %v (senders=%v), want exactly 4 Saved Messages commands", invoked, senders)
	}
	for i, s := range senders {
		if s != selfID {
			t.Fatalf("senders[%d] = %d, want %d", i, s, selfID)
		}
	}
}
