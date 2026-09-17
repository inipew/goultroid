package deeplink

import "errors"

var (
	// ErrTokenNotFound indicates the token hash does not match any record.
	ErrTokenNotFound = errors.New("deeplink: token not found")

	// ErrTokenExpired indicates the token validity period has passed.
	ErrTokenExpired = errors.New("deeplink: token has expired")

	// ErrTokenConsumed indicates the token has already been redeemed.
	ErrTokenConsumed = errors.New("deeplink: token already consumed")

	// ErrTokenScopeMismatch indicates the token was redeemed by an unauthorized user.
	ErrTokenScopeMismatch = errors.New("deeplink: user scope mismatch")

	// ErrTokenGenerationStale indicates the plugin generation that issued the token has unloaded.
	ErrTokenGenerationStale = errors.New("deeplink: token generation is stale")

	// ErrPayloadTooLarge indicates the supplied payload exceeds the 8 KiB limit.
	ErrPayloadTooLarge = errors.New("deeplink: payload exceeds maximum allowed size")

	// ErrInvalidPurpose indicates the token purpose is invalid or empty.
	ErrInvalidPurpose = errors.New("deeplink: invalid purpose")

	// ErrInvalidScreenKey indicates the screen key is invalid for open screen purpose.
	ErrInvalidScreenKey = errors.New("deeplink: screen key is invalid for open screen purpose")
)
