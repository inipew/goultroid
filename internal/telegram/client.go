package telegram

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

// Client wraps the gotd MTProto Telegram client and manages updates and authentication.
type Client struct {
	raw         *telegram.Client
	cfg         *config.Config
	dispatcher  *Dispatcher
	gaps        *updates.Manager
	peerManager *peers.Manager
	peerStorage *PeerStorage
	executor    *RPCExecutor
	logger      *zap.Logger
	ready       chan struct{}
	readyOnce   sync.Once
}

func (c *Client) Ready() <-chan struct{} {
	if c.ready == nil {
		return nil
	}
	return c.ready
}

func (c *Client) IsReady() bool {
	if c.ready == nil {
		return false
	}
	select {
	case <-c.ready:
		return true
	default:
		return false
	}
}

func (c *Client) signalReady() {
	c.readyOnce.Do(func() { close(c.ready) })
}

// terminalAuth handles interactive CLI login with phone, SMS/app OTP code, and 2FA password.
type terminalAuth struct {
	phone string
}

func (t terminalAuth) Phone(ctx context.Context) (string, error) {
	return t.phone, nil
}

func (t terminalAuth) Password(ctx context.Context) (string, error) {
	fmt.Print("Enter 2FA Password: ")
	reader := bufio.NewReader(os.Stdin)
	pass, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(pass), nil
}

func (t terminalAuth) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (t terminalAuth) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("signup not supported: please register on the official Telegram app")
}

func (t terminalAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter Telegram Login Code: ")
	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(code), nil
}

// NewClient creates and configures a new Telegram client instance.
func NewClient(cfg *config.Config, dispatcher *Dispatcher, db *database.DB, logger *zap.Logger) (*Client, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	// Ensure session directory exists
	sessionDir := filepath.Dir(cfg.SessionFile)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create session directory: %w", err)
	}

	// Setup update hooks
	tgDispatcher := tg.NewUpdateDispatcher()
	dispatcher.RegisterHooks(&tgDispatcher)

	var updateHook telegram.UpdateHandler

	raw := telegram.NewClient(
		cfg.AppID,
		cfg.AppHash,
		telegram.Options{
			SessionStorage: &telegram.FileSessionStorage{
				Path: cfg.SessionFile,
			},
			UpdateHandler: telegram.UpdateHandlerFunc(func(ctx context.Context, u tg.UpdatesClass) error {
				if updateHook != nil {
					return updateHook.Handle(ctx, u)
				}
				return nil
			}),
		},
	)

	var peerStorage *PeerStorage
	if db != nil {
		peerStorage = NewPeerStorage(db)
	}
	peerManager := peers.Options{
		Storage: peerStorage,
	}.Build(raw.API())
	gaps := updates.New(updates.Config{
		Handler:      tgDispatcher,
		AccessHasher: peerManager,
	})
	updateHook = peerManager.UpdateHook(gaps)

	limiter := NewHierarchicalRPCLimiter(DefaultHierarchicalLimiterConfig())
	executor, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter:       limiter,
		DefaultPolicy: DefaultExecutorPolicy,
		Metrics:       NewInMemoryRPCMetrics(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize rpc executor: %w", err)
	}

	return &Client{
		raw:         raw,
		cfg:         cfg,
		dispatcher:  dispatcher,
		gaps:        gaps,
		peerManager: peerManager,
		peerStorage: peerStorage,
		executor:    executor,
		logger:      logger,
		ready:       make(chan struct{}),
	}, nil
}

// Executor returns the RPCExecutor for coordinating outbound Telegram calls.
func (c *Client) Executor() *RPCExecutor {
	if c != nil {
		return c.executor
	}
	return nil
}

// API returns the raw Telegram MTProto client.
func (c *Client) API() *tg.Client {
	return c.raw.API()
}

// Service returns the TelegramServicer instance.
func (c *Client) Service() core.TelegramServicer {
	if c != nil && c.dispatcher != nil {
		return c.dispatcher.Service()
	}
	return nil
}

// Dispatcher returns the underlying Dispatcher instance.
func (c *Client) Dispatcher() *Dispatcher {
	if c != nil {
		return c.dispatcher
	}
	return nil
}

// Resolver returns the configured PeerResolver instance.
func (c *Client) Resolver() core.PeerResolver {
	if c != nil && c.dispatcher != nil {
		return c.dispatcher.Resolver()
	}
	return nil
}

// PreloadDialogs preloads initial dialogs to populate access hashes in peer manager.
func (c *Client) PreloadDialogs(ctx context.Context) error {
	if c == nil || c.peerManager == nil || c.raw == nil {
		return nil
	}
	dialogs, err := ExecuteRPC(ctx, c.executor, RPCMeta{
		Method: "messages.getDialogs",
		Family: "messages",
		Kind:   RPCReadOnly,
	}, func(opCtx context.Context) (tg.MessagesDialogsClass, error) {
		return c.raw.API().MessagesGetDialogs(opCtx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      100,
		})
	})
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("failed to preload dialogs for channel access hashes", zap.Error(err))
		}
		return err
	}
	if d, ok := dialogs.AsModified(); ok {
		_ = c.peerManager.Apply(ctx, d.GetUsers(), d.GetChats())
	}
	return nil
}

// RPCMetrics returns a snapshot of RPC metrics collected so far.
func (c *Client) RPCMetrics() RPCMetricsSnapshot {
	if c != nil && c.executor != nil {
		if inMem, ok := c.executor.Metrics().(*InMemoryRPCMetrics); ok {
			return inMem.Snapshot()
		}
	}
	return RPCMetricsSnapshot{}
}

// ResolverCacheLen returns the number of active entries in the resolver peer cache.
func (c *Client) ResolverCacheLen() int {
	if c != nil && c.dispatcher != nil {
		if r, ok := c.dispatcher.Resolver().(*Resolver); ok && r != nil {
			return r.Len()
		}
	}
	return 0
}

// Run connects to Telegram, performs authentication, and maintains the update loop.
func (c *Client) Run(ctx context.Context) error {
	return c.raw.Run(ctx, func(ctx context.Context) error {
		// Initialize service wrapper & peer resolver
		svc := NewService(c.raw.API())
		svc.SetExecutor(c.executor)
		svc.SetPeerManager(c.peerManager)
		if c.peerStorage != nil {
			svc.SetStorage(c.peerStorage)
		}
		c.dispatcher.SetService(svc)
		resolver := NewResolver(c.raw.API(), c.peerManager)
		if c.logger != nil {
			resolver.SetLogger(c.logger.Named("resolver"))
		}
		resolver.SetExecutor(c.executor)
		if c.peerStorage != nil {
			resolver.SetStorage(c.peerStorage)
		}
		svc.SetResolver(resolver)
		c.dispatcher.SetResolver(resolver)

		// Authenticate if needed
		status, err := c.raw.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("failed to check auth status: %w", err)
		}

		if !status.Authorized {
			flow := auth.NewFlow(
				terminalAuth{phone: c.cfg.Phone},
				auth.SendCodeOptions{},
			)
			if err := c.raw.Auth().IfNecessary(ctx, flow); err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}
		}

		// Fetch self user info
		me, err := c.raw.Self(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch self user: %w", err)
		}

		svc.SetSelfID(me.ID)
		c.dispatcher.SetSelfID(me.ID)
		c.signalReady()

		if c.peerManager != nil {
			if err := c.peerManager.Init(ctx); err != nil && c.logger != nil {
				c.logger.Warn("failed to initialize peer manager", zap.Error(err))
			}
		}

		if c.logger != nil {
			c.logger.Info("connected to Telegram",
				zap.String("name", me.FirstName+" "+me.LastName),
				zap.String("username", me.Username),
				zap.Int64("id", me.ID),
			)
		}

		// Check if process was restarted and notify origin chat
		go checkRestartState(ctx, svc, c.logger)

		// Run update recovery manager until context cancellation
		return c.gaps.Run(ctx, c.raw.API(), me.ID, updates.AuthOptions{IsBot: me.Bot})
	})
}

// checkRestartState checks data/restart.json to edit the restart message if present.
func checkRestartState(ctx context.Context, svc core.TelegramServicer, logger *zap.Logger) {
	restartPath := "data/restart.json"
	data, err := os.ReadFile(restartPath)
	if err != nil {
		return
	}
	defer os.Remove(restartPath)

	type restartState struct {
		PeerType   string `json:"peer_type"`
		ChatID     int64  `json:"chat_id"`
		IsChannel  bool   `json:"is_channel"`
		AccessHash int64  `json:"access_hash"`
		MsgID      int    `json:"msg_id"`
		Time       int64  `json:"time"`
	}

	var state restartState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}

	if state.MsgID == 0 && state.ChatID == 0 && state.PeerType == "" {
		return
	}

	var peer tg.InputPeerClass
	switch state.PeerType {
	case "self":
		peer = &tg.InputPeerSelf{}
	case "user":
		peer = &tg.InputPeerUser{UserID: state.ChatID, AccessHash: state.AccessHash}
	case "channel":
		peer = &tg.InputPeerChannel{ChannelID: state.ChatID, AccessHash: state.AccessHash}
	case "chat":
		peer = &tg.InputPeerChat{ChatID: state.ChatID}
	default:
		// Backward compatibility for legacy restart.json
		if state.IsChannel {
			peer = &tg.InputPeerChannel{ChannelID: state.ChatID, AccessHash: state.AccessHash}
		} else if state.ChatID == 0 {
			peer = &tg.InputPeerSelf{}
		} else if state.AccessHash != 0 {
			peer = &tg.InputPeerUser{UserID: state.ChatID, AccessHash: state.AccessHash}
		} else {
			peer = &tg.InputPeerChat{ChatID: state.ChatID}
		}
	}

	elapsed := time.Since(time.Unix(state.Time, 0)).Round(time.Millisecond)
	msg := fmt.Sprintf("✅ <b>GoUltroid restarted successfully!</b> (took <i>%s</i>)", elapsed)

	var editErr error
	if state.MsgID != 0 {
		editErr = svc.EditMessage(ctx, peer, state.MsgID, msg)
	}

	// Fallback to sending a new message if editing fails or no msgID
	if state.MsgID == 0 || editErr != nil {
		if _, sendErr := svc.SendMessage(ctx, peer, msg); sendErr != nil {
			if logger != nil {
				logger.Warn("failed to send/edit restart confirmation message",
					zap.NamedError("editErr", editErr),
					zap.NamedError("sendErr", sendErr),
				)
			}
		}
	}
}
