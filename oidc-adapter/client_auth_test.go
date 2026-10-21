package zitadel

import (
	"context"
	"errors"
	"testing"
)

func TestClientAuthentication(t *testing.T) {
	for _, method := range []string{"none", "basic"} {
		for _, invalidPKCE := range []bool{false, true} {
			name := method
			if invalidPKCE {
				name += "/invalid-pkce"
			}
			t.Run(name, func(t *testing.T) {
				p := newFixture(t)
				cfg := testConfig()
				if method == "none" {
					cfg.ClientSecret = ""
					p.publicClient = true
				}
				c := p.client(cfg, memory(t))
				query := p.begin(c)
				if invalidPKCE {
					p.challenge = "different-challenge"
				}
				result, err := c.Complete(context.Background(), query)
				if invalidPKCE {
					var diagnostic *AuthenticationError
					if result != nil || !errors.As(err, &diagnostic) || diagnostic.Stage != "token_exchange" || diagnostic.Code != "invalid_grant" {
						t.Fatalf("unexpected PKCE failure: %v", err)
					}
				} else if err != nil || result == nil || result.Identity.Subject != "immutable-user-id" {
					t.Fatalf("login failed: %v", err)
				}
				if p.exchanges.Load() != 1 {
					t.Fatal("token exchange retried with another authentication method")
				}
			})
		}
	}
}

func TestClientAuthenticationFailureDoesNotFallback(t *testing.T) {
	p := newFixture(t)
	cfg := testConfig()
	cfg.ClientSecret = "incorrect-secret"
	c := p.client(cfg, memory(t))
	result, err := c.Complete(context.Background(), p.begin(c))
	var diagnostic *AuthenticationError
	if result != nil || !errors.As(err, &diagnostic) || diagnostic.Stage != "token_exchange" ||
		diagnostic.Code != "invalid_client" || p.exchanges.Load() != 1 {
		t.Fatalf("client authentication failure retried or accepted: %v", err)
	}
}

func TestClientAuthenticationChangeInvalidatesLogin(t *testing.T) {
	for _, initialSecret := range []string{"", "test-client-secret"} {
		t.Run("initial-secret="+initialSecret, func(t *testing.T) {
			p := newFixture(t)
			store := memory(t)
			cfg := testConfig()
			cfg.ClientSecret = initialSecret
			original := p.client(cfg, store)
			query := p.begin(original)
			cfg.ClientSecret = "test-client-secret"
			if initialSecret != "" {
				cfg.ClientSecret = ""
			}
			updated := p.client(cfg, store)
			result, err := updated.Complete(context.Background(), query)
			if result != nil || !errors.Is(err, ErrTransaction) || p.exchanges.Load() != 0 {
				t.Fatal("authentication method change accepted a pending login")
			}
		})
	}
}
