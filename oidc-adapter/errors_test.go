package zitadel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSafeAuthenticationDiagnostics(t *testing.T) {
	for _, name := range []string{"invalid client", "untrusted provider error", "nonce", "signature", "email", "missing project roles", "state"} {
		t.Run(name, func(t *testing.T) {
			p := newFixture(t)
			stage, code, status := "", "", 0
			switch name {
			case "invalid client":
				p.tokenError = true
				p.tokenErrorCode = "invalid_client"
				stage, code, status = "token_exchange", "invalid_client", 400
			case "untrusted provider error":
				p.tokenError = true
				p.tokenErrorCode = "SECRET-in-provider-error"
				stage, code, status = "token_exchange", "provider_request_failed", 400
			case "nonce":
				p.claims["nonce"] = "SECRET-wrong-nonce"
				stage, code = "token_validation", "nonce_mismatch"
			case "signature":
				p.wrongKey = true
				stage, code = "token_validation", "signature_verification_failed"
			case "email":
				p.claims["email_verified"] = false
				stage, code = "email", "email_unverified_missing_or_invalid"
			case "missing project roles":
				p.claims[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("123")] = nil
				stage, code = "role_mapping", "project_role_claim_missing"
			case "state":
				stage, code = "state", "invalid_expired_or_consumed_state"
			}
			c := p.client(testConfig(), memory(t))
			query := p.begin(c)
			if name == "state" {
				query.Set("state", "invalid")
			}
			result, err := c.Complete(context.Background(), query)
			var diagnostic *AuthenticationError
			if result != nil || !errors.As(err, &diagnostic) {
				t.Fatalf("missing diagnostic: %v", err)
			}
			if diagnostic.Stage != stage || diagnostic.Code != code || diagnostic.HTTPStatus != status {
				t.Fatalf("unexpected diagnostic: %+v", diagnostic)
			}
			serialized, _ := json.Marshal(diagnostic)
			for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), string(serialized)} {
				for _, secret := range []string{"SECRET", testConfig().ClientSecret, "provider-access-token", query.Get("state")} {
					if secret != "invalid" && strings.Contains(text, secret) {
						t.Fatal("diagnostic leaked authentication data")
					}
				}
			}
		})
	}
}

func TestUntrustedAlgorithmDiagnostic(t *testing.T) {
	for _, algorithm := range []string{"EdDSA", "SECRET-from-token-header"} {
		data, _ := json.Marshal(map[string]string{"alg": algorithm})
		raw := base64.RawURLEncoding.EncodeToString(data) + ".payload.signature"
		code := tokenValidationFailure(raw, errors.New("SECRET-from-verifier"))
		if strings.Contains(code, "SECRET") {
			t.Fatal("untrusted token header or verifier error leaked")
		}
		if algorithm == "EdDSA" && code != "unsupported_signing_algorithm_EdDSA" {
			t.Fatal("algorithm diagnostic missing")
		}
	}
}

func TestProjectGrantDiagnosticsPreserveOrganizationBoundary(t *testing.T) {
	for _, name := range []string{"signed grant", "userinfo grant", "unmapped role", "other project", "untrusted organization ID", "bad signature"} {
		t.Run(name, func(t *testing.T) {
			p := newFixture(t)
			grants := map[string]any{"operator": map[string]string{"789": "SECRET-org-domain"}}
			p.claims[projectRolesClaim("123")] = grants
			want := []string{"789"}
			switch name {
			case "userinfo grant":
				p.claims[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("123")] = grants
			case "unmapped role":
				p.claims[projectRolesClaim("123")] = map[string]any{"unmapped": map[string]string{"789": "SECRET-org-domain"}}
				want = nil
			case "other project":
				p.claims[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("123")] = nil
				p.userinfo[projectRolesClaim("999")] = grants
				want = nil
			case "untrusted organization ID":
				p.claims[projectRolesClaim("123")] = map[string]any{"operator": map[string]string{"SECRET-org-id": "SECRET-org-domain"}}
				want = nil
			case "bad signature":
				p.wrongKey = true
				want = nil
			}
			c := p.client(testConfig(), memory(t))
			query := p.begin(c)
			result, err := c.Complete(context.Background(), query)
			var diagnostic *AuthenticationError
			if result != nil || !errors.Is(err, ErrDenied) || !errors.As(err, &diagnostic) {
				t.Fatalf("other organization granted access or lost failure: %v", err)
			}
			if !reflect.DeepEqual(diagnostic.RoleGrantOrganizations, want) {
				t.Fatalf("unexpected grant diagnostics: %v", diagnostic.RoleGrantOrganizations)
			}
			if len(want) > 0 && (diagnostic.Stage != "role_mapping" || diagnostic.Code != "mapped_role_granted_in_other_organization") {
				t.Fatal("missing grant organization mismatch diagnostic")
			}
			data, _ := json.Marshal(diagnostic)
			if strings.Contains(string(data), "SECRET") {
				t.Fatal("grant diagnostics leaked unneeded or untrusted claim values")
			}
			if _, err := c.Complete(context.Background(), query); !errors.Is(err, ErrTransaction) {
				t.Fatal("failed grant check left reusable callback state")
			}
		})
	}
}

func TestGrantOrganizationDiagnosticsAreBounded(t *testing.T) {
	orgs := map[string]string{}
	for id := 100; id < 140; id++ {
		orgs[fmt.Sprint(id)] = "org.example"
	}
	data, _ := json.Marshal([]any{map[string]any{"operator": orgs}, map[string]any{"operator": orgs}})
	got := mappedRoleGrantOrganizations(data, testConfig())
	if len(got) != 16 || got[0] != "100" || got[15] != "115" {
		t.Fatalf("unbounded, duplicate, or unstable diagnostic IDs: %v", got)
	}
}
