package zitadel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUsernameClaimConfiguration(t *testing.T) {
	for _, claim := range []string{"", "email", "preferred_username", "sub", "urn:example:username"} {
		cfg := testConfig()
		cfg.UsernameClaim = claim
		validated, err := ValidateConfig(cfg)
		if err != nil || validated.UsernameClaim != claim {
			t.Fatalf("username claim did not survive validation: %v", err)
		}
	}
	for _, claim := range []string{" ", " name", "display name", "name\n", "name\x00", strings.Repeat("x", 257)} {
		cfg := testConfig()
		cfg.UsernameClaim = claim
		if _, err := ValidateConfig(cfg); err == nil {
			t.Fatal("invalid username claim configuration accepted")
		}
	}
}

func TestUsernameClaimAuthentication(t *testing.T) {
	for _, tt := range []struct {
		name           string
		claim          string
		idToken        map[string]any
		userInfo       map[string]any
		userInfoStatus int
		want           string
		wantError      error
	}{
		{name: "unset retains host naming", idToken: map[string]any{"preferred_username": "ignored"}},
		{name: "preferred username", claim: "preferred_username", idToken: map[string]any{"preferred_username": "Operator_1@EXAMPLE.com"}, want: "Operator_1@EXAMPLE.com"},
		{name: "verified email", claim: "email", want: "operator@example.com"},
		{name: "subject", claim: "sub", want: "immutable-user-id"},
		{name: "custom claim", claim: "urn:example:username", idToken: map[string]any{"urn:example:username": "team:alice+ops"}, want: "team:alice+ops"},
		{name: "userinfo fallback", claim: "preferred_username", userInfo: map[string]any{"preferred_username": "alice"}, want: "alice"},
		{name: "missing does not fall back to email", claim: "preferred_username", wantError: ErrDenied},
		{name: "userinfo unavailable fails login", claim: "preferred_username", userInfoStatus: 503, wantError: ErrProvider},
		{name: "userinfo subject mismatch", claim: "preferred_username", userInfo: map[string]any{"preferred_username": "alice", "sub": "someone-else"}, wantError: ErrDenied},
		{name: "signed value takes precedence", claim: "preferred_username", idToken: map[string]any{"preferred_username": "signed-alice", projectRolesClaim("123"): nil}, userInfo: map[string]any{"preferred_username": "userinfo-alice"}, want: "signed-alice"},
		{name: "empty signed value cannot be replaced", claim: "preferred_username", idToken: map[string]any{"preferred_username": ""}, userInfo: map[string]any{"preferred_username": "alice"}, wantError: ErrDenied},
		{name: "null signed value cannot be replaced", claim: "preferred_username", idToken: map[string]any{"preferred_username": json.RawMessage("null")}, wantError: ErrDenied},
		{name: "object value", claim: "preferred_username", idToken: map[string]any{"preferred_username": map[string]string{"value": "alice"}}, wantError: ErrDenied},
		{name: "array value", claim: "preferred_username", userInfo: map[string]any{"preferred_username": []string{"alice"}}, wantError: ErrDenied},
		{name: "numeric value", claim: "preferred_username", idToken: map[string]any{"preferred_username": 123}, wantError: ErrDenied},
		{name: "email verification still required", claim: "preferred_username", idToken: map[string]any{"preferred_username": "alice", "email_verified": false}, wantError: ErrDenied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newFixture(t)
			p.claims, p.userinfo, p.userInfoStatus = tt.idToken, tt.userInfo, tt.userInfoStatus
			cfg := testConfig()
			cfg.UsernameClaim = tt.claim
			client := p.client(cfg, memory(t))
			result, err := client.Complete(context.Background(), p.begin(client))
			// The fixture omits default profile fields, causing one lookup.
			if got := p.userInfoCalls.Load(); got != 1 {
				t.Fatalf("UserInfo calls = %d, want 1", got)
			}
			if tt.wantError != nil {
				if result != nil || !errors.Is(err, tt.wantError) {
					t.Fatalf("expected %v, got %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			identity := result.Identity
			if identity.Username != tt.want || identity.Email != "operator@example.com" || identity.DisplayName != identity.Email || identity.Subject != "immutable-user-id" {
				t.Fatalf("unexpected account name, display label, or verified identity: %+v", identity)
			}
		})
	}
}

func TestUsernameClaimRejectsUnsafeNames(t *testing.T) {
	for _, value := range []string{"", " ", " alice", "alice ", "alice smith", "alice/bob", "alice\\bob", ".", "..", "Zoë", "alice\nadmin", "alice\x00", strings.Repeat("x", 257)} {
		p := newFixture(t)
		p.claims["preferred_username"] = value
		cfg := testConfig()
		cfg.UsernameClaim = "preferred_username"
		client := p.client(cfg, memory(t))
		result, err := client.Complete(context.Background(), p.begin(client))
		if result != nil || !errors.Is(err, ErrDenied) {
			t.Fatal("unsafe account name accepted")
		}
	}
}

func TestUsernameClaimIsIndependentOfDisplayLabel(t *testing.T) {
	p := newFixture(t)
	p.claims["preferred_username"] = "alice"
	p.claims["name"] = "Alice Example"
	cfg := testConfig()
	cfg.UsernameClaim, cfg.DisplayNameClaim = "preferred_username", "name"
	client := p.client(cfg, memory(t))
	result, err := client.Complete(context.Background(), p.begin(client))
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity.Username != "alice" || result.Identity.DisplayName != "Alice Example" || result.Identity.Email != "operator@example.com" {
		t.Fatal("account name, display label, and verified email were not kept separate")
	}
}
