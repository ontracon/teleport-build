package zitadel

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Transaction contains sensitive PKCE material and trusted host request data.
// A shared implementation must protect it as authentication state.
type Transaction struct {
	State    string
	Nonce    string
	Verifier string
	// Binding identifies the immutable issuer/client/policy configuration.
	Binding   string
	CreatedAt time.Time
	ExpiresAt time.Time
	// Request is an opaque host request (e.g. Teleport's original auth request).
	// It must be supplied by the host, never restored from callback parameters.
	Request []byte
}

// Store must implement atomic, create-only Put and atomic take-once Consume.
// Consume must remove or mark the record consumed, including expired records.
// Replicas must share
// a backend and the same time basis. An implementation must honor context.
type Store interface {
	Put(context.Context, Transaction) error
	Consume(context.Context, string) (Transaction, error)
}

// MemoryStore is bounded process-local storage for development and tests.
// It cannot support callbacks arriving at another Auth replica or a restart.
type MemoryStore struct {
	mu       sync.Mutex
	entries  map[string]Transaction
	capacity int
	now      func() time.Time
}

func NewMemoryStore(capacity int) (*MemoryStore, error) {
	if capacity < 1 {
		return nil, errors.New("transaction capacity must be positive")
	}
	return &MemoryStore{entries: make(map[string]Transaction), capacity: capacity, now: time.Now}, nil
}

func (s *MemoryStore) Put(ctx context.Context, tx Transaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	for key, item := range s.entries {
		if !now.Before(item.ExpiresAt) {
			delete(s.entries, key)
		}
	}
	if tx.State == "" || !now.Before(tx.ExpiresAt) {
		return ErrTransaction
	}
	if _, exists := s.entries[tx.State]; exists {
		return ErrTransaction
	}
	if len(s.entries) >= s.capacity {
		return ErrCapacity
	}
	tx.Request = slices.Clone(tx.Request)
	s.entries[tx.State] = tx
	return nil
}

func (s *MemoryStore) Consume(ctx context.Context, state string) (Transaction, error) {
	if err := ctx.Err(); err != nil {
		return Transaction{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Transaction{}, err
	}
	tx, ok := s.entries[state]
	delete(s.entries, state)
	if !ok || !s.now().Before(tx.ExpiresAt) {
		return Transaction{}, ErrTransaction
	}
	return tx, nil
}
