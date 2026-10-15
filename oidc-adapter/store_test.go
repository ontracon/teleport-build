package zitadel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCapacityExpiryAndOwnership(t *testing.T) {
	store, err := NewMemoryStore(1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.now = func() time.Time { return now }
	tx := Transaction{State: "one", ExpiresAt: now.Add(time.Minute), Request: []byte("original")}
	if err := store.Put(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	tx.Request[0] = 'X'
	if err := store.Put(context.Background(), tx); !errors.Is(err, ErrTransaction) {
		t.Fatal("duplicate state overwritten")
	}
	other := Transaction{State: "two", ExpiresAt: now.Add(time.Hour)}
	if err := store.Put(context.Background(), other); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity ignored: %v", err)
	}
	saved, err := store.Consume(context.Background(), "one")
	if err != nil || string(saved.Request) != "original" {
		t.Fatalf("caller mutated stored request: %q %v", saved.Request, err)
	}
	if err := store.Put(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := store.Consume(context.Background(), "one"); !errors.Is(err, ErrTransaction) {
		t.Fatal("expiry boundary accepted")
	}
	if err := store.Put(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if err := store.Put(context.Background(), Transaction{State: "three", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("expired capacity not reclaimed: %v", err)
	}
}

func TestMemoryStoreCancellation(t *testing.T) {
	store := memory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, Transaction{State: "one", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled put accepted")
	}
	if _, err := store.Consume(ctx, "one"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled consume accepted")
	}
}
