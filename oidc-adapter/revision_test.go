package zitadel

import (
	"context"
	"errors"
	"testing"
)

func TestConnectorRevisionInvalidatesOutstandingLogin(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	cfg := testConfig()
	cfg.PolicyRevision = "revision-1"
	original := p.client(cfg, store)
	query := p.begin(original)
	cfg.PolicyRevision = "revision-2"
	updated := p.client(cfg, store)
	result, err := updated.Complete(context.Background(), query)
	if result != nil || !errors.Is(err, ErrTransaction) || p.exchanges.Load() != 0 {
		t.Fatal("changed connector policy accepted a previous login or exchanged its code")
	}
	if _, err := original.Complete(context.Background(), query); !errors.Is(err, ErrTransaction) {
		t.Fatal("old policy revived a consumed transaction")
	}
}

func TestDisplayNameClaimInvalidatesOutstandingLogin(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	cfg := testConfig()
	original := p.client(cfg, store)
	query := p.begin(original)
	cfg.DisplayNameClaim = "preferred_username"
	updated := p.client(cfg, store)
	result, err := updated.Complete(context.Background(), query)
	if result != nil || !errors.Is(err, ErrTransaction) || p.exchanges.Load() != 0 {
		t.Fatal("changed display name claim accepted an outstanding login")
	}
}

func TestUsernameClaimInvalidatesOutstandingLogin(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	cfg := testConfig()
	original := p.client(cfg, store)
	query := p.begin(original)
	cfg.UsernameClaim = "preferred_username"
	updated := p.client(cfg, store)
	result, err := updated.Complete(context.Background(), query)
	if result != nil || !errors.Is(err, ErrTransaction) || p.exchanges.Load() != 0 {
		t.Fatal("changed username claim accepted an outstanding login")
	}
}

func TestTraitClaimsInvalidateOutstandingLogin(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	cfg := testConfig()
	original := p.client(cfg, store)
	query := p.begin(original)
	cfg.TraitClaims = []string{"groups"}
	updated := p.client(cfg, store)
	result, err := updated.Complete(context.Background(), query)
	if result != nil || !errors.Is(err, ErrTransaction) || p.exchanges.Load() != 0 {
		t.Fatal("changed trait allowlist accepted an outstanding login")
	}
}
