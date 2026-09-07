package peer

import (
	"errors"
)

var (
	// ErrPeerResolution indicates failure to resolve a generic Telegram peer into an InputPeer.
	ErrPeerResolution = errors.New("assistant/peer: unable to resolve peer")

	// ErrAccessHashMissing indicates that the access hash is zero or not found in cache/entities.
	ErrAccessHashMissing = errors.New("assistant/peer: access hash missing")

	// ErrUnsupportedPeer indicates an unrecognized or unsupported peer variant.
	ErrUnsupportedPeer = errors.New("assistant/peer: unsupported peer type")
)
