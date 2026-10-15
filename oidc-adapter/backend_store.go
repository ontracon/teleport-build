package zitadel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// AtomicBackend is the small backend contract needed for cross-replica login
// transactions. Implementations must compare the entire old value atomically,
// enforce expiry, and protect stored authentication secrets. Create must not
// overwrite an existing key. CompareAndSwap must never recreate a missing key.
//
// Backend errors indicating a missing key must wrap ErrBackendNotFound;
// duplicate Create and failed comparisons must wrap ErrBackendConflict.
// Other failures are propagated, never mistaken for successful consumption.
type AtomicBackend interface {
	Create(ctx context.Context, key string, value []byte, expiresAt time.Time) error
	Get(ctx context.Context, key string) ([]byte, error)
	CompareAndSwap(ctx context.Context, key string, old, replacement []byte, expiresAt time.Time) error
}

var (
	ErrBackendNotFound = errors.New("OIDC backend key not found")
	ErrBackendConflict = errors.New("OIDC backend comparison failed")
)

// BackendStore provides Store using an atomic shared backend. Consumed records
// become tombstones until their original expiry, so replay or a duplicate Put
// cannot resurrect a pending transaction. It holds no process-local state.
type BackendStore struct {
	backend AtomicBackend
	now     func() time.Time
}

func NewBackendStore(backend AtomicBackend) (*BackendStore, error) {
	if backend == nil {
		return nil, errors.New("atomic backend is required")
	}
	return &BackendStore{backend: backend, now: time.Now}, nil
}

func transactionKey(state string) string {
	// Hash untrusted lookup strings before constructing a backend path.
	digest := sha256.Sum256([]byte(state))
	return "zitadel/transactions/" + hex.EncodeToString(digest[:])
}

func (s *BackendStore) Put(ctx context.Context, tx Transaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx.State == "" || !s.now().Before(tx.ExpiresAt) {
		return ErrTransaction
	}
	value, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	err = s.backend.Create(ctx, transactionKey(tx.State), value, tx.ExpiresAt)
	if errors.Is(err, ErrBackendConflict) {
		return ErrTransaction
	}
	return err
}

func (s *BackendStore) Consume(ctx context.Context, state string) (Transaction, error) {
	if err := ctx.Err(); err != nil {
		return Transaction{}, err
	}
	if state == "" {
		return Transaction{}, ErrTransaction
	}
	key := transactionKey(state)
	value, err := s.backend.Get(ctx, key)
	if errors.Is(err, ErrBackendNotFound) {
		return Transaction{}, ErrTransaction
	}
	if err != nil {
		return Transaction{}, err
	}
	var tx Transaction
	if err := json.Unmarshal(value, &tx); err != nil || tx.State != state || tx.ExpiresAt.IsZero() {
		return Transaction{}, ErrTransaction
	}
	// TTL is unchanged: a consumed record can neither extend authentication
	// state lifetime nor lose the one-use marker before its original expiry.
	err = s.backend.CompareAndSwap(ctx, key, value, []byte(`{"consumed":true}`), tx.ExpiresAt)
	if errors.Is(err, ErrBackendConflict) || errors.Is(err, ErrBackendNotFound) {
		return Transaction{}, ErrTransaction
	}
	if err != nil {
		return Transaction{}, err
	}
	if !s.now().Before(tx.ExpiresAt) {
		return Transaction{}, ErrTransaction
	}
	return tx, nil
}
