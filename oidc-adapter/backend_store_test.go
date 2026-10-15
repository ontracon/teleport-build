package zitadel

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type backendEntry struct {
	value   []byte
	expires time.Time
}

// testBackend implements the same atomic contract as a shared persistent
// backend. Multiple independent BackendStores use it to simulate Auth replicas.
type testBackend struct {
	mu       sync.Mutex
	entries  map[string]backendEntry
	now      func() time.Time
	casError error
}

func newTestBackend() *testBackend {
	return &testBackend{entries: make(map[string]backendEntry), now: time.Now}
}

func (b *testBackend) Create(ctx context.Context, key string, value []byte, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if item, exists := b.entries[key]; exists && b.now().Before(item.expires) {
		return ErrBackendConflict
	}
	b.entries[key] = backendEntry{value: slices.Clone(value), expires: expires}
	return nil
}

func (b *testBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item, exists := b.entries[key]
	if !exists || !b.now().Before(item.expires) {
		return nil, ErrBackendNotFound
	}
	return slices.Clone(item.value), nil
}

func (b *testBackend) CompareAndSwap(ctx context.Context, key string, old, replacement []byte, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.casError != nil {
		return b.casError
	}
	item, exists := b.entries[key]
	if !exists || !b.now().Before(item.expires) {
		return ErrBackendNotFound
	}
	if !bytes.Equal(item.value, old) {
		return ErrBackendConflict
	}
	b.entries[key] = backendEntry{value: slices.Clone(replacement), expires: expires}
	return nil
}

func TestBackendStoreCrossReplicaConsumption(t *testing.T) {
	backend := newTestBackend()
	writer, _ := NewBackendStore(backend)
	tx := Transaction{State: "unique-state", ExpiresAt: time.Now().Add(time.Minute), Request: []byte("trusted-request")}
	if err := writer.Put(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var successful atomic.Int32
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replica, _ := NewBackendStore(backend)
			result, err := replica.Consume(context.Background(), tx.State)
			if err == nil {
				if string(result.Request) != "trusted-request" {
					t.Error("host request lost")
				}
				successful.Add(1)
			} else if !errors.Is(err, ErrTransaction) {
				t.Errorf("unexpected consumption error: %v", err)
			}
		}()
	}
	wg.Wait()
	if successful.Load() != 1 {
		t.Fatalf("consumed %d times", successful.Load())
	}
	if err := writer.Put(context.Background(), tx); !errors.Is(err, ErrTransaction) {
		t.Fatal("consumed state resurrected by duplicate Put")
	}
	if item := backend.entries[transactionKey(tx.State)]; !item.expires.Equal(tx.ExpiresAt) {
		t.Fatal("tombstone changed expiry")
	}
}

func TestBackendStoreExpiryAndFailures(t *testing.T) {
	backend := newTestBackend()
	store, _ := NewBackendStore(backend)
	now := time.Now()
	store.now = func() time.Time { return now }
	backend.now = func() time.Time { return now }
	tx := Transaction{State: "one", ExpiresAt: now.Add(time.Minute)}
	if err := store.Put(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	backend.casError = errors.New("storage unavailable")
	if _, err := store.Consume(context.Background(), tx.State); err != backend.casError {
		t.Fatal("backend failure hidden")
	}
	backend.casError = nil
	now = tx.ExpiresAt
	if _, err := store.Consume(context.Background(), tx.State); !errors.Is(err, ErrTransaction) {
		t.Fatal("expiry accepted")
	}
	if err := store.Put(context.Background(), tx); !errors.Is(err, ErrTransaction) {
		t.Fatal("expired put accepted")
	}
	if _, err := store.Consume(context.Background(), "missing"); !errors.Is(err, ErrTransaction) {
		t.Fatal("missing state accepted")
	}
}

func TestClientCompletesOnDifferentReplica(t *testing.T) {
	p := newFixture(t)
	backend := newTestBackend()
	firstStore, _ := NewBackendStore(backend)
	secondStore, _ := NewBackendStore(backend)
	first := p.client(testConfig(), firstStore)
	second := p.client(testConfig(), secondStore)
	q := p.begin(first)
	if _, err := second.Complete(context.Background(), q); err != nil {
		t.Fatalf("different replica failed: %v", err)
	}
	if _, err := first.Complete(context.Background(), q); !errors.Is(err, ErrTransaction) {
		t.Fatal("originating replica accepted replay")
	}
}
