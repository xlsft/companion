package contracts

import (
	"context"
	"errors"
)

// SecretRef is the only form of a secret stored in SQLite or settings
// (FR-BYOK-003), e.g. "provider/3f2a…/api_key".
type SecretRef string

var ErrSecretNotFound = errors.New("secret not found")

// SecretStore wraps Keychain, Credential Manager or Secret Service.
// Values never pass through logs or error messages.
type SecretStore interface {
	Put(ctx context.Context, ref SecretRef, value []byte) error
	Get(ctx context.Context, ref SecretRef) ([]byte, error)
	Delete(ctx context.Context, ref SecretRef) error
}
