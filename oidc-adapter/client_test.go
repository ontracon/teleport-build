package zitadel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// The fixture speaks discovery, token, JWKS, and UserInfo through an in-memory
// HTTPS transport. Tests exercise the real OAuth2 exchange and JWT verifier
// without external credentials, listeners, or network access.
type fakeIDP struct {
	t              *testing.T
	key            *rsa.PrivateKey
	kid            string
	nonce          string
	challenge      string
	claims         map[string]any
	userinfo       map[string]any
	userInfoStatus int
	userInfoCalls  atomic.Int32
	discovery      map[string]any
	tokenError     bool
	tokenErrorCode string
	wrongKey       bool
	exchanges      atomic.Int32
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data)))}
}

func testConfig() Config {
	return Config{
		IssuerURL: "https://idp.example", ClientID: "teleport-client", ClientSecret: "test-client-secret",
		RedirectURL: "https://teleport.example/v1/webapi/oidc/callback", ProjectID: "123", OrganizationID: "456",
		RoleMappings: map[string][]string{"operator": {"access"}, "administrator": {"editor", "access"}},
	}
}

func newFixture(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeIDP{
		t: t, key: key, kid: "initial-key", claims: map[string]any{}, userinfo: map[string]any{},
		discovery: map[string]any{
			"issuer": "https://idp.example", "authorization_endpoint": "https://idp.example/authorize",
			"token_endpoint": "https://idp.example/token", "jwks_uri": "https://idp.example/keys",
			"userinfo_endpoint": "https://idp.example/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"},
		},
	}
}

func (p *fakeIDP) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" || req.URL.Host != "idp.example" {
		return nil, errors.New("unexpected endpoint")
	}
	switch req.URL.Path {
	case "/.well-known/openid-configuration":
		return response(200, p.discovery), nil
	case "/keys":
		key := jose.JSONWebKey{Key: &p.key.PublicKey, KeyID: p.kid, Algorithm: "RS256", Use: "sig"}
		return response(200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}}), nil
	case "/token":
		p.exchanges.Add(1)
		id, secret, ok := req.BasicAuth()
		if !ok || id != "teleport-client" || secret != "test-client-secret" {
			return response(401, map[string]any{"error": "invalid_client"}), nil
		}
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		verifier := req.Form.Get("code_verifier")
		sum := sha256.Sum256([]byte(verifier))
		if verifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge ||
			req.Form.Get("code") != "valid-code" || req.Form.Get("grant_type") != "authorization_code" ||
			req.Form.Get("redirect_uri") != testConfig().RedirectURL {
			return response(400, map[string]any{"error": "invalid_grant"}), nil
		}
		if p.tokenError {
			code := p.tokenErrorCode
			if code == "" {
				code = "invalid_grant"
			}
			return response(400, map[string]any{"error": code, "error_description": "SECRET-provider-token-details"}), nil
		}
		claims := map[string]any{
			"iss": "https://idp.example", "sub": "immutable-user-id", "aud": "teleport-client", "azp": "teleport-client",
			"iat": time.Now().Add(-time.Second).Unix(), "exp": time.Now().Add(time.Minute).Unix(), "nonce": p.nonce,
			"email": "operator@example.com", "email_verified": true,
			projectRolesClaim("123"): map[string]any{"operator": map[string]string{"456": "org.example"}},
		}
		for key, value := range p.claims {
			if value == nil {
				delete(claims, key)
			} else {
				claims[key] = value
			}
		}
		key := p.key
		if p.wrongKey {
			var err error
			key, err = rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				return nil, err
			}
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", p.kid))
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(claims)
		jwt, err := signer.Sign(payload)
		if err != nil {
			return nil, err
		}
		serialized, err := jwt.CompactSerialize()
		if err != nil {
			return nil, err
		}
		return response(200, map[string]any{"access_token": "provider-access-token", "token_type": "Bearer", "id_token": serialized}), nil
	case "/userinfo":
		p.userInfoCalls.Add(1)
		if p.userInfoStatus != 0 {
			return response(p.userInfoStatus, map[string]any{"error": "userinfo_unavailable"}), nil
		}
		if req.Header.Get("Authorization") != "Bearer provider-access-token" {
			return response(401, nil), nil
		}
		info := map[string]any{"sub": "immutable-user-id", "email": "operator@example.com", "email_verified": true,
			projectRolesClaim("123"): map[string]any{"operator": map[string]string{"456": "org.example"}}}
		for key, value := range p.userinfo {
			if value == nil {
				delete(info, key)
			} else {
				info[key] = value
			}
		}
		return response(200, info), nil
	default:
		return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
	}
}

func (p *fakeIDP) client(cfg Config, store Store) *Client {
	p.t.Helper()
	c, err := New(context.Background(), cfg, store, &http.Client{Transport: p})
	if err != nil {
		p.t.Fatal(err)
	}
	return c
}

func memory(t *testing.T) *MemoryStore {
	t.Helper()
	s, err := NewMemoryStore(100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (p *fakeIDP) begin(c *Client) url.Values {
	p.t.Helper()
	a, err := c.Begin(context.Background(), []byte(`{"connector_id":"Zitadel","public_key":"trusted-host-key"}`))
	if err != nil {
		p.t.Fatal(err)
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	p.nonce = q.Get("nonce")
	p.challenge = q.Get("code_challenge")
	if p.nonce == "" || p.challenge == "" || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
		p.t.Fatal("authorization request missing security parameters")
	}
	if q.Get("client_secret") != "" || q.Get("code_verifier") != "" {
		p.t.Fatal("secret in authorization URL")
	}
	if q.Get("redirect_uri") != testConfig().RedirectURL {
		p.t.Fatal("incorrect redirect")
	}
	for _, scope := range []string{"openid", "urn:zitadel:iam:org:projects:roles", "urn:zitadel:iam:org:project:id:123:aud"} {
		if !strings.Contains(q.Get("scope"), scope) {
			p.t.Fatalf("missing scope %s", scope)
		}
	}
	if strings.Contains(q.Get("scope"), "urn:zitadel:iam:org:roles:id:") {
		p.t.Fatal("provider role filter hides grants needed for organization diagnostics")
	}
	return url.Values{"state": {a.State}, "code": {"valid-code"}}
}

func TestSuccessfulAuthentication(t *testing.T) {
	p := newFixture(t)
	c := p.client(testConfig(), memory(t))
	q := p.begin(c)
	result, err := c.Complete(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Identity, Identity{Issuer: "https://idp.example", Subject: "immutable-user-id", Email: "operator@example.com", DisplayName: "operator@example.com", Roles: []string{"access"}, Traits: map[string][]string{
		"email": {"operator@example.com"}, "iss": {"https://idp.example"}, "sub": {"immutable-user-id"},
	}}) {
		t.Fatalf("unexpected identity: %+v", result.Identity)
	}
	if string(result.Request) != `{"connector_id":"Zitadel","public_key":"trusted-host-key"}` || result.AuthenticatedAt.IsZero() || result.TokenExpiry.IsZero() {
		t.Fatal("missing trusted request or login metadata")
	}
	if _, err := c.Complete(context.Background(), q); !errors.Is(err, ErrTransaction) {
		t.Fatalf("replay accepted: %v", err)
	}
	if p.exchanges.Load() != 1 {
		t.Fatal("replay reached token exchange")
	}
}

func TestRejectInvalidIDTokens(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value any
	}{
		{"issuer", "iss", "https://attacker.example"},
		{"audience", "aud", "another-client"},
		{"expiry", "exp", time.Now().Add(-time.Minute).Unix()},
		{"missing expiry", "exp", nil},
		{"nonce", "nonce", "wrong-nonce"},
		{"missing nonce", "nonce", nil},
		{"missing subject", "sub", nil},
		{"missing issued at", "iat", nil},
		{"future issued at", "iat", time.Now().Add(time.Hour).Unix()},
		{"authorized party", "azp", "another-client"},
		{"unverified email", "email_verified", false},
		{"email boolean type", "email_verified", "true"},
		{"access token hash", "at_hash", "wrong-hash"},
		{"wrong organization", projectRolesClaim("123"), map[string]any{"operator": map[string]string{"999": "elsewhere.example"}}},
		{"unmapped role", projectRolesClaim("123"), map[string]any{"root": map[string]string{"456": "org.example"}}},
		{"malformed roles", projectRolesClaim("123"), []string{"operator"}},
		{"null roles", projectRolesClaim("123"), json.RawMessage("null")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFixture(t)
			p.claims[tc.key] = tc.value
			c := p.client(testConfig(), memory(t))
			q := p.begin(c)
			if result, err := c.Complete(context.Background(), q); result != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid token accepted: result=%+v error=%v", result, err)
			}
			if _, err := c.Complete(context.Background(), q); !errors.Is(err, ErrTransaction) {
				t.Fatal("failed authentication left reusable state")
			}
		})
	}
}

func TestUserInfoAndAudience(t *testing.T) {
	for _, name := range []string{"userinfo fallback", "userinfo subject mismatch", "other project only", "multi audience with azp", "multi audience without azp", "signed denial wins", "partial email mismatch"} {
		t.Run(name, func(t *testing.T) {
			p := newFixture(t)
			denied := false
			switch name {
			case "userinfo fallback":
				p.claims[projectRolesClaim("123")] = nil
				p.claims["email"] = nil
				p.claims["email_verified"] = nil
			case "userinfo subject mismatch":
				p.claims[projectRolesClaim("123")] = nil
				p.userinfo["sub"] = "another-user"
				denied = true
			case "other project only":
				p.claims[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("999")] = map[string]any{"operator": map[string]string{"456": "org.example"}}
				p.userinfo["urn:zitadel:iam:org:project:roles"] = map[string]any{"operator": map[string]string{"456": "org.example"}}
				denied = true
			case "multi audience with azp":
				p.claims["aud"] = []string{"teleport-client", "123"}
			case "multi audience without azp":
				p.claims["aud"] = []string{"teleport-client", "123"}
				p.claims["azp"] = nil
				denied = true
			case "signed denial wins":
				p.claims["email_verified"] = false
				p.claims[projectRolesClaim("123")] = nil
				denied = true
			case "partial email mismatch":
				p.claims["email_verified"] = nil
				p.userinfo["email"] = "another-email@example.com"
				denied = true
			}
			c := p.client(testConfig(), memory(t))
			q := p.begin(c)
			result, err := c.Complete(context.Background(), q)
			if denied {
				if result != nil || !errors.Is(err, ErrDenied) {
					t.Fatalf("expected denial: result=%+v error=%v", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvalidSignatureAndKeyRotation(t *testing.T) {
	p := newFixture(t)
	c := p.client(testConfig(), memory(t))
	q := p.begin(c)
	if _, err := c.Complete(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	p.wrongKey = true
	q = p.begin(c)
	if _, err := c.Complete(context.Background(), q); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong signature accepted: %v", err)
	}
	p.wrongKey = false
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.key, p.kid = key, "rotated-key"
	q = p.begin(c)
	if _, err := c.Complete(context.Background(), q); err != nil {
		t.Fatalf("key rotation failed: %v", err)
	}
}

func TestConcurrentCallbackConsumedOnce(t *testing.T) {
	p := newFixture(t)
	c := p.client(testConfig(), memory(t))
	q := p.begin(c)
	var successful atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Complete(context.Background(), q)
			if err == nil {
				successful.Add(1)
			} else if !errors.Is(err, ErrTransaction) {
				t.Errorf("unexpected concurrent callback error: %v", err)
			}
		}()
	}
	wg.Wait()
	if successful.Load() != 1 || p.exchanges.Load() != 1 {
		t.Fatalf("successes=%d exchanges=%d", successful.Load(), p.exchanges.Load())
	}
}

func TestCallbackValidationAndSanitizedErrors(t *testing.T) {
	for _, name := range []string{"duplicate state", "duplicate code", "missing code", "provider denial", "response issuer", "token failure"} {
		t.Run(name, func(t *testing.T) {
			p := newFixture(t)
			c := p.client(testConfig(), memory(t))
			q := p.begin(c)
			switch name {
			case "duplicate state":
				q.Add("state", q.Get("state"))
			case "duplicate code":
				q.Add("code", "another-code")
			case "missing code":
				q.Del("code")
			case "provider denial":
				q.Set("error", "access_denied")
				q.Set("error_description", "SECRET-error-details")
			case "response issuer":
				q.Set("iss", "https://attacker.example")
			case "token failure":
				p.tokenError = true
			}
			result, err := c.Complete(context.Background(), q)
			if result != nil || err == nil {
				t.Fatal("invalid callback accepted")
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "test-client-secret") {
				t.Fatal("provider secrets leaked in error")
			}
			if name != "token failure" && p.exchanges.Load() != 0 {
				t.Fatal("invalid callback reached token exchange")
			}
		})
	}
}

func TestPolicyChangeInvalidatesOutstandingLogin(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	c := p.client(testConfig(), store)
	q := p.begin(c)
	cfg := testConfig()
	cfg.RoleMappings["operator"] = []string{"editor"}
	changed := p.client(cfg, store)
	if _, err := changed.Complete(context.Background(), q); !errors.Is(err, ErrTransaction) {
		t.Fatalf("old policy transaction accepted: %v", err)
	}
	if p.exchanges.Load() != 0 {
		t.Fatal("changed policy reached token exchange")
	}
}

func TestFreshAuthenticationAndAMR(t *testing.T) {
	for _, name := range []string{"valid", "missing auth_time", "old auth_time", "future auth_time", "missing amr"} {
		t.Run(name, func(t *testing.T) {
			p := newFixture(t)
			cfg := testConfig()
			cfg.MaxAuthenticationAge = time.Minute
			cfg.RequiredAMR = []string{"mfa"}
			p.claims["auth_time"] = time.Now().Add(-time.Second).Unix()
			p.claims["amr"] = []string{"pwd", "mfa"}
			switch name {
			case "missing auth_time":
				p.claims["auth_time"] = nil
			case "old auth_time":
				p.claims["auth_time"] = time.Now().Add(-time.Hour).Unix()
			case "future auth_time":
				p.claims["auth_time"] = time.Now().Add(time.Hour).Unix()
			case "missing amr":
				p.claims["amr"] = []string{"pwd"}
			}
			c := p.client(cfg, memory(t))
			q := p.begin(c)
			if _, err := c.Complete(context.Background(), q); (err != nil) != (name != "valid") {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestDiscoveryEndpointPolicy(t *testing.T) {
	for _, endpoint := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri", "userinfo_endpoint"} {
		for _, value := range []string{"http://idp.example/insecure", "https://attacker.example/endpoint", "https://user:secret@idp.example/endpoint"} {
			t.Run(endpoint+value, func(t *testing.T) {
				p := newFixture(t)
				p.discovery[endpoint] = value
				if _, err := New(context.Background(), testConfig(), memory(t), &http.Client{Transport: p}); err == nil {
					t.Fatal("unsafe discovery accepted")
				}
			})
		}
	}
}

func TestTransactionExpiryDuringExchange(t *testing.T) {
	p := newFixture(t)
	store := memory(t)
	c := p.client(testConfig(), store)
	q := p.begin(c)
	c.http.Transport.(*providerTransport).inner = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := p.RoundTrip(req)
		if req.URL.Path == "/token" {
			c.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
		}
		return resp, err
	})
	if _, err := c.Complete(context.Background(), q); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired transaction accepted: %v", err)
	}
}
