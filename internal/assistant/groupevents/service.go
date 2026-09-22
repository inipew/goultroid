package groupevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

const (
	Namespace        = "assistant_group_events"
	WelcomeKey       = "welcome"
	GoodbyeKey       = "goodbye"
	MaxTemplateBytes = 2048
	maxRenderedUsers = 8
	preloadLimit     = 10_000
)

var (
	ErrUnavailable     = fmt.Errorf("%w: assistant group events unavailable", core.ErrUnavailable)
	ErrInvalidKind     = fmt.Errorf("%w: invalid group event kind", core.ErrInvalidArgs)
	ErrInvalidTemplate = fmt.Errorf("%w: invalid group event template", core.ErrInvalidArgs)
)

type Transport interface {
	SendMessage(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error)
}

type Config struct {
	Enabled  bool
	Template string
}

type State struct {
	Kind     core.GroupServiceKind
	Config   Config
	Revision uint64
}

type chatConfig struct {
	welcome State
	goodbye State
}

type Service struct {
	store  core.GroupStateStore
	reader core.GroupStateNamespaceReader
	bus    *core.EventBus

	mu          sync.RWMutex
	chats       map[int64]chatConfig
	activeChats int
	transport   Transport
	sub         *core.Subscription
	loaded      bool
}

func New(store core.GroupStateStore, bus *core.EventBus) *Service {
	reader, _ := store.(core.GroupStateNamespaceReader)
	return &Service{
		store:  store,
		reader: reader,
		bus:    bus,
		chats:  make(map[int64]chatConfig),
	}
}

func keyFor(kind core.GroupServiceKind) (string, error) {
	switch kind {
	case core.GroupServiceMemberJoined:
		return WelcomeKey, nil
	case core.GroupServiceMemberLeft:
		return GoodbyeKey, nil
	default:
		return "", ErrInvalidKind
	}
}

func defaultTemplate(kind core.GroupServiceKind) string {
	switch kind {
	case core.GroupServiceMemberJoined:
		return "👋 Welcome {user} to <b>{chat}</b>!"
	case core.GroupServiceMemberLeft:
		return "👋 Goodbye {user}."
	default:
		return ""
	}
}

func normalizeConfig(kind core.GroupServiceKind, config Config) (Config, error) {
	if _, err := keyFor(kind); err != nil {
		return Config{}, err
	}
	config.Template = strings.TrimSpace(config.Template)
	if len(config.Template) > MaxTemplateBytes {
		return Config{}, fmt.Errorf("%w: template exceeds %d bytes", ErrInvalidTemplate, MaxTemplateBytes)
	}
	if config.Template == "" {
		config.Template = defaultTemplate(kind)
	}
	return config, nil
}

func decodeConfig(kind core.GroupServiceKind, raw []byte) (Config, error) {
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, fmt.Errorf("%w: decode %s config: %v", ErrInvalidTemplate, kind, err)
	}
	return normalizeConfig(kind, config)
}

func (s *Service) Load(ctx context.Context) error {
	if s == nil || s.store == nil || s.reader == nil || s.bus == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, err := s.reader.ListNamespace(ctx, Namespace, preloadLimit)
	if err != nil {
		return fmt.Errorf("load assistant group event state: %w", err)
	}

	chats := make(map[int64]chatConfig)
	for _, record := range records {
		var kind core.GroupServiceKind
		switch record.Key {
		case WelcomeKey:
			kind = core.GroupServiceMemberJoined
		case GoodbyeKey:
			kind = core.GroupServiceMemberLeft
		default:
			continue
		}
		config, err := decodeConfig(kind, record.Value)
		if err != nil {
			return fmt.Errorf("load assistant group event state chat=%d key=%s: %w", record.ChatID, record.Key, err)
		}
		current := chats[record.ChatID]
		state := State{Kind: kind, Config: config, Revision: record.Revision}
		if kind == core.GroupServiceMemberJoined {
			current.welcome = state
		} else {
			current.goodbye = state
		}
		chats[record.ChatID] = current
	}

	activeChats := 0
	for _, config := range chats {
		if enabledChat(config) {
			activeChats++
		}
	}

	s.mu.Lock()
	s.chats = chats
	s.activeChats = activeChats
	s.loaded = true
	s.syncSubscriptionLocked()
	s.mu.Unlock()
	return nil
}

func (s *Service) SetTransport(transport Transport) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.transport = transport
	s.mu.Unlock()
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

func enabledChat(config chatConfig) bool {
	return config.welcome.Config.Enabled || config.goodbye.Config.Enabled
}

func (s *Service) hasEnabledLocked() bool {
	return s.activeChats > 0
}

func (s *Service) syncSubscriptionLocked() {
	if s.bus == nil {
		return
	}
	active := s.loaded && s.hasEnabledLocked()
	if active && s.sub == nil {
		s.sub = s.bus.SubscribeWithOptions(
			core.EventTypeGroupService,
			s.handleEvent,
			core.SubscribeOptions{
				Owner:       "assistant:groupevents",
				Timeout:     5 * time.Second,
				MinPriority: core.PriorityNormal,
			},
		)
		return
	}
	if !active && s.sub != nil {
		sub := s.sub
		s.sub = nil
		sub.Close()
	}
}

func (s *Service) stateLocked(chatID int64, kind core.GroupServiceKind) State {
	config := s.chats[chatID]
	if kind == core.GroupServiceMemberJoined {
		state := config.welcome
		if state.Kind == "" {
			state.Kind = kind
			state.Config.Template = defaultTemplate(kind)
		}
		return state
	}
	state := config.goodbye
	if state.Kind == "" {
		state.Kind = kind
		state.Config.Template = defaultTemplate(kind)
	}
	return state
}

func (s *Service) State(chatID int64, kind core.GroupServiceKind) (State, error) {
	if s == nil || chatID <= 0 {
		return State{}, ErrUnavailable
	}
	if _, err := keyFor(kind); err != nil {
		return State{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stateLocked(chatID, kind), nil
}

func (s *Service) Interested(chatID int64, kind core.GroupServiceKind) bool {
	if s == nil || chatID <= 0 {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return false
	}
	return s.stateLocked(chatID, kind).Config.Enabled
}

func (s *Service) applyState(chatID int64, state State) {
	s.mu.Lock()
	current := s.chats[chatID]
	wasActive := enabledChat(current)
	if state.Kind == core.GroupServiceMemberJoined {
		current.welcome = state
	} else {
		current.goodbye = state
	}
	isActive := enabledChat(current)
	switch {
	case !wasActive && isActive:
		s.activeChats++
	case wasActive && !isActive && s.activeChats > 0:
		s.activeChats--
	}
	// Retain disabled durable state as well. Status/revision must reflect the
	// persisted P7-F row rather than falling back to synthetic defaults.
	s.chats[chatID] = current
	s.syncSubscriptionLocked()
	s.mu.Unlock()
}

func (s *Service) Configure(
	ctx *core.Context,
	kind core.GroupServiceKind,
	enabled bool,
	template string,
) (State, error) {
	if s == nil || s.store == nil || ctx == nil || ctx.Chat == nil {
		return State{}, ErrUnavailable
	}
	key, err := keyFor(kind)
	if err != nil {
		return State{}, err
	}

	current := Config{Template: defaultTemplate(kind)}
	var revision uint64
	record, err := ctx.GetGroupState(Namespace, key)
	switch {
	case err == nil:
		revision = record.Revision
		decoded, decodeErr := decodeConfig(kind, record.Value)
		if decodeErr != nil {
			return State{}, decodeErr
		}
		current = decoded
	case errors.Is(err, core.ErrNotFound):
	default:
		return State{}, err
	}

	template = strings.TrimSpace(template)
	if template != "" {
		current.Template = template
	}
	current.Enabled = enabled
	current, err = normalizeConfig(kind, current)
	if err != nil {
		return State{}, err
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return State{}, fmt.Errorf("encode group event config: %w", err)
	}

	record, err = ctx.CompareAndSwapGroupState(
		core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator},
		Namespace,
		key,
		revision,
		encoded,
		0,
	)
	if err != nil {
		return State{}, err
	}
	state := State{Kind: kind, Config: current, Revision: record.Revision}
	s.applyState(ctx.Chat.ID, state)
	return state, nil
}

func (s *Service) Reset(ctx *core.Context, kind core.GroupServiceKind) (State, error) {
	return s.Configure(ctx, kind, true, defaultTemplate(kind))
}

func (s *Service) Publish(event *core.GroupServiceEvent) {
	if s == nil || event == nil || event.ChatID <= 0 || len(event.Users) == 0 {
		return
	}
	if !s.Interested(event.ChatID, event.Kind) {
		return
	}
	if s.bus == nil || !s.bus.HasSubscribers(core.EventTypeGroupService) {
		return
	}
	s.bus.Publish(event)
}

func displayUser(user core.GroupServiceUser) string {
	label := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if label == "" {
		if user.Username != "" {
			label = "@" + strings.TrimPrefix(user.Username, "@")
		} else {
			label = "User " + strconv.FormatInt(user.ID, 10)
		}
	}
	label = html.EscapeString(label)
	if user.ID <= 0 {
		return label
	}
	return fmt.Sprintf("<a href=\"tg://user?id=%d\">%s</a>", user.ID, label)
}

func renderUsers(users []core.GroupServiceUser) string {
	if len(users) == 0 {
		return "someone"
	}
	limit := len(users)
	if limit > maxRenderedUsers {
		limit = maxRenderedUsers
	}
	parts := make([]string, 0, limit+1)
	for _, user := range users[:limit] {
		parts = append(parts, displayUser(user))
	}
	if len(users) > limit {
		parts = append(parts, fmt.Sprintf("and %d more", len(users)-limit))
	}
	return strings.Join(parts, ", ")
}

func renderTemplate(template string, event *core.GroupServiceEvent) string {
	if event == nil || len(event.Users) == 0 {
		return ""
	}
	replacements := strings.NewReplacer(
		"{user}", renderUsers(event.Users),
		"{user_id}", strconv.FormatInt(event.Users[0].ID, 10),
		"{chat}", html.EscapeString(strings.TrimSpace(event.ChatTitle)),
		"{count}", strconv.Itoa(len(event.Users)),
	)
	return replacements.Replace(template)
}

func (s *Service) handleEvent(ctx context.Context, raw core.Event) error {
	event, ok := raw.(*core.GroupServiceEvent)
	if !ok || event == nil || event.ChatID <= 0 || len(event.Users) == 0 {
		return nil
	}

	s.mu.RLock()
	state := s.stateLocked(event.ChatID, event.Kind)
	transport := s.transport
	s.mu.RUnlock()
	if !state.Config.Enabled {
		return nil
	}
	if transport == nil || event.Peer == nil {
		return ErrUnavailable
	}
	text := renderTemplate(state.Config.Template, event)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := transport.SendMessage(ctx, event.Peer, text, nil)
	return err
}
