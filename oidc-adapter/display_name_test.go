package zitadel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDisplayNameClaimConfiguration(t *testing.T) {
	for _, claim := range []string{"", "email", "preferred_username", "name", "nickname", "https://idp.example/display-name", "profile.name"} {
		cfg := testConfig()
		cfg.DisplayNameClaim = claim
		validated, err := ValidateConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if claim == "" {
			claim = "email"
		}
		if validated.DisplayNameClaim != claim {
			t.Fatal("display name claim lost during validation")
		}
	}
	for _, claim := range []string{" ", " name", "display name", "name\n", "name\x00", strings.Repeat("x", 257)} {
		cfg := testConfig()
		cfg.DisplayNameClaim = claim
		if _, err := ValidateConfig(cfg); err == nil {
			t.Fatal("invalid display name claim accepted")
		}
	}
}

func TestDisplayNameClaims(t *testing.T) {
	for _, tt := range []struct {
		name           string
		claim          string
		idToken        map[string]any
		userInfo       map[string]any
		userInfoStatus int
		want           string
		denied         bool
	}{
		{name: "default verified email", want: "operator@example.com"},
		{name: "preferred username", claim: "preferred_username", idToken: map[string]any{"preferred_username": "operator"}, want: "operator"},
		{name: "full name", claim: "name", idToken: map[string]any{"name": "  Zoë Example  "}, want: "Zoë Example"},
		{name: "nickname", claim: "nickname", idToken: map[string]any{"nickname": "ops"}, want: "ops"},
		{name: "custom claim", claim: "https://idp.example/label", idToken: map[string]any{"https://idp.example/label": "Custom Label"}, want: "Custom Label"},
		{name: "userinfo fallback", claim: "preferred_username", userInfo: map[string]any{"preferred_username": "from-userinfo"}, want: "from-userinfo"},
		{name: "missing claim", claim: "name", want: "operator@example.com"},
		{name: "empty signed claim wins", claim: "name", idToken: map[string]any{"name": " "}, userInfo: map[string]any{"name": "Other Name"}, want: "operator@example.com"},
		{name: "wrong signed type", claim: "name", idToken: map[string]any{"name": map[string]any{"value": "nested"}}, want: "operator@example.com"},
		{name: "wrong userinfo type", claim: "name", userInfo: map[string]any{"name": []string{"one", "two"}}, want: "operator@example.com"},
		{name: "control characters", claim: "name", idToken: map[string]any{"name": "User\nName"}, want: "operator@example.com"},
		{name: "oversized label", claim: "name", idToken: map[string]any{"name": strings.Repeat("x", 257)}, want: "operator@example.com"},
		{name: "optional userinfo unavailable", claim: "name", userInfoStatus: 503, want: "operator@example.com"},
		{name: "required userinfo unavailable", claim: "name", idToken: map[string]any{"email": nil}, userInfoStatus: 503, denied: true},
		{name: "userinfo subject mismatch", claim: "name", userInfo: map[string]any{"name": "Other User", "sub": "other-user"}, denied: true},
		{name: "signed claim wins during required lookup", claim: "name", idToken: map[string]any{"name": "Signed Name", projectRolesClaim("123"): nil}, userInfo: map[string]any{"name": "Other Name"}, want: "Signed Name"},
		{name: "userinfo email cannot replace signed email", claim: "preferred_username", userInfo: map[string]any{"preferred_username": "operator", "email": "different@example.com"}, want: "operator"},
		{name: "username does not bypass email verification", claim: "preferred_username", idToken: map[string]any{"preferred_username": "operator", "email_verified": false}, denied: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newFixture(t)
			p.claims, p.userinfo, p.userInfoStatus = tt.idToken, tt.userInfo, tt.userInfoStatus
			cfg := testConfig()
			cfg.DisplayNameClaim = tt.claim
			client := p.client(cfg, memory(t))
			result, err := client.Complete(context.Background(), p.begin(client))
			// The fixture omits default profile fields, causing one lookup.
			if got := p.userInfoCalls.Load(); got != 1 {
				t.Fatalf("UserInfo calls = %d, want 1", got)
			}
			if tt.denied {
				if result != nil || (!errors.Is(err, ErrDenied) && !errors.Is(err, ErrProvider)) {
					t.Fatalf("expected authentication rejection, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Identity.DisplayName != tt.want || result.Identity.Email != "operator@example.com" || result.Identity.Subject != "immutable-user-id" {
				t.Fatalf("display label or authenticated identity changed: %+v", result.Identity)
			}
		})
	}
}
