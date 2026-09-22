package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	MaxGroupStateNamespaceBytes = 64
	MaxGroupStateKeyBytes       = 128
	MaxGroupStateValueBytes     = 64 * 1024
)

// GroupStateKey is always scoped to one concrete Telegram chat. There is no
// global/effective fallback for this domain.
type GroupStateKey struct {
	ChatID    int64
	Namespace string
	Key       string
}

// GroupStateRecord is one durable, optimistic-revisioned manager state value.
type GroupStateRecord struct {
	GroupStateKey
	Value     []byte
	Revision  uint64
	UpdatedBy int64
	UpdatedAt time.Time
	ExpiresAt *time.Time
}

// GroupStateCAS is an exact optimistic mutation. ExpectedRevision=0 means
// create-only; an existing row is a conflict rather than an implicit overwrite.
type GroupStateCAS struct {
	GroupStateKey
	ExpectedRevision uint64
	Value            []byte
	UpdatedBy        int64
	UpdatedAt        time.Time
	ExpiresAt        *time.Time
}

// GroupStateDelete is an exact-revision delete.
type GroupStateDelete struct {
	GroupStateKey
	ExpectedRevision uint64
	DeletedBy        int64
	DeletedAt        time.Time
}

// GroupStateWriteGrant is an opaque proof that fresh contextual authorization
// succeeded for one chat/actor immediately before persistence. Its fields are
// private so callers outside core cannot manufacture a valid grant.
type GroupStateWriteGrant struct {
	chatID     int64
	actorID    int64
	authorized bool
}

func newGroupStateWriteGrant(chatID, actorID int64) GroupStateWriteGrant {
	return GroupStateWriteGrant{chatID: chatID, actorID: actorID, authorized: true}
}

// Authorizes reports whether the opaque grant matches the exact persistence
// coordinate actor. Store implementations must reject invalid grants.
func (g GroupStateWriteGrant) Authorizes(chatID, actorID int64) bool {
	return g.authorized && g.chatID > 0 && g.actorID > 0 &&
		g.chatID == chatID && g.actorID == actorID
}

// GroupStateStore is the persistent boundary consumed by Assistant commands.
// Implementations must not implement scope fallback to global/user settings.
type GroupStateStore interface {
	Get(context.Context, GroupStateKey) (GroupStateRecord, error)
	CompareAndSwap(context.Context, GroupStateWriteGrant, GroupStateCAS) (GroupStateRecord, error)
	DeleteCompareAndSwap(context.Context, GroupStateWriteGrant, GroupStateDelete) error
	PruneExpired(context.Context, time.Time, int) (int, error)
	Count(context.Context) (int, error)
}

// AttachGroupStateStore wires the composition-owned persistence service without
// exposing it as a public Context field to feature handlers.
func AttachGroupStateStore(c *Context, store GroupStateStore) {
	if c != nil {
		c.groupStateStore = store
	}
}

func normalizedGroupStateCoordinate(namespace, key string) (string, string, error) {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	key = strings.ToLower(strings.TrimSpace(key))
	if namespace == "" || len(namespace) > MaxGroupStateNamespaceBytes {
		return "", "", fmt.Errorf("%w: invalid group-state namespace", ErrInvalidArgs)
	}
	if key == "" || len(key) > MaxGroupStateKeyBytes {
		return "", "", fmt.Errorf("%w: invalid group-state key", ErrInvalidArgs)
	}
	return namespace, key, nil
}

func (c *Context) groupStateKey(namespace, key string) (GroupStateKey, error) {
	if c == nil || c.Chat == nil || !c.IsManagerGroup() {
		return GroupStateKey{}, ErrGroupOnly
	}
	namespace, key, err := normalizedGroupStateCoordinate(namespace, key)
	if err != nil {
		return GroupStateKey{}, err
	}
	if c.Chat.ID <= 0 {
		return GroupStateKey{}, fmt.Errorf("%w: invalid group chat id", ErrInvalidArgs)
	}
	return GroupStateKey{ChatID: c.Chat.ID, Namespace: namespace, Key: key}, nil
}

// GetGroupState reads only the current chat's explicit state. It never falls
// back to global/user settings.
func (c *Context) GetGroupState(namespace, key string) (GroupStateRecord, error) {
	coordinate, err := c.groupStateKey(namespace, key)
	if err != nil {
		return GroupStateRecord{}, err
	}
	if c.groupStateStore == nil {
		return GroupStateRecord{}, fmt.Errorf("%w: group state store is not configured", ErrUnavailable)
	}
	return c.groupStateStore.Get(c.Ctx, coordinate)
}

func validateGroupStateWriteRequirement(requirement GroupAuthorizationRequirement) error {
	if err := requirement.Validate(); err != nil {
		return err
	}
	switch requirement.Level {
	case GroupAuthorizationAdministrator, GroupAuthorizationCreator:
		return nil
	default:
		return fmt.Errorf("%w: group-state persistence requires administrator or creator authority", ErrGroupAuthorizationDenied)
	}
}

func (c *Context) authorizeGroupStateWrite(requirement GroupAuthorizationRequirement) (GroupStateWriteGrant, error) {
	if err := validateGroupStateWriteRequirement(requirement); err != nil {
		return GroupStateWriteGrant{}, err
	}
	snapshot, err := c.ResolveGroupActor(true)
	if err != nil {
		return GroupStateWriteGrant{}, err
	}
	if err := requirement.Authorize(snapshot.Principal); err != nil {
		return GroupStateWriteGrant{}, err
	}
	return newGroupStateWriteGrant(c.Chat.ID, c.SenderID()), nil
}

// CompareAndSwapGroupState performs fresh contextual authorization immediately
// before persistence. expectedRevision=0 is create-only.
func (c *Context) CompareAndSwapGroupState(
	requirement GroupAuthorizationRequirement,
	namespace, key string,
	expectedRevision uint64,
	value []byte,
	ttl time.Duration,
) (GroupStateRecord, error) {
	coordinate, err := c.groupStateKey(namespace, key)
	if err != nil {
		return GroupStateRecord{}, err
	}
	if len(value) > MaxGroupStateValueBytes {
		return GroupStateRecord{}, fmt.Errorf("%w: group-state value exceeds %d bytes", ErrInvalidArgs, MaxGroupStateValueBytes)
	}
	if ttl < 0 {
		return GroupStateRecord{}, fmt.Errorf("%w: negative group-state ttl", ErrInvalidArgs)
	}
	if c.groupStateStore == nil {
		return GroupStateRecord{}, fmt.Errorf("%w: group state store is not configured", ErrUnavailable)
	}
	grant, err := c.authorizeGroupStateWrite(requirement)
	if err != nil {
		return GroupStateRecord{}, err
	}

	now := time.Now().UTC()
	var expiresAt *time.Time
	if ttl > 0 {
		expiry := now.Add(ttl)
		expiresAt = &expiry
	}
	return c.groupStateStore.CompareAndSwap(c.Ctx, grant, GroupStateCAS{
		GroupStateKey:    coordinate,
		ExpectedRevision: expectedRevision,
		Value:            append([]byte(nil), value...),
		UpdatedBy:        c.SenderID(),
		UpdatedAt:        now,
		ExpiresAt:        expiresAt,
	})
}

// DeleteGroupState performs fresh contextual authorization immediately before
// an exact-revision delete.
func (c *Context) DeleteGroupState(
	requirement GroupAuthorizationRequirement,
	namespace, key string,
	expectedRevision uint64,
) error {
	coordinate, err := c.groupStateKey(namespace, key)
	if err != nil {
		return err
	}
	if expectedRevision == 0 {
		return fmt.Errorf("%w: delete requires a non-zero revision", ErrInvalidArgs)
	}
	if c.groupStateStore == nil {
		return fmt.Errorf("%w: group state store is not configured", ErrUnavailable)
	}
	grant, err := c.authorizeGroupStateWrite(requirement)
	if err != nil {
		return err
	}
	return c.groupStateStore.DeleteCompareAndSwap(c.Ctx, grant, GroupStateDelete{
		GroupStateKey:    coordinate,
		ExpectedRevision: expectedRevision,
		DeletedBy:        c.SenderID(),
		DeletedAt:        time.Now().UTC(),
	})
}
