package zitadel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTraitClaimConfiguration(t *testing.T) {
	cfg := testConfig()
	names := []string{"groups", "amr", "groups", "urn:example:department"}
	cfg.TraitClaims = names
	validated, err := ValidateConfig(cfg)
	if err != nil || !slices.Equal(validated.TraitClaims, []string{"amr", "groups", "urn:example:department"}) {
		t.Fatalf("trait allowlist did not survive validation: %v", err)
	}
	names[0] = "changed-after-validation"
	if slices.Contains(validated.TraitClaims, names[0]) {
		t.Fatal("configuration aliases the caller's allowlist")
	}
	for _, name := range []string{"", "bad claim", "bad\n", strings.Repeat("x", 257), "zitadel_subject", "zitadel_issuer", "access_token", "refresh_token", "id_token", "client_secret", "nonce", "code_verifier"} {
		cfg.TraitClaims = []string{name}
		if _, err := ValidateConfig(cfg); err == nil {
			t.Fatalf("invalid/reserved claim name accepted: %q", name)
		}
	}
	cfg.TraitClaims = make([]string, 33)
	if _, err := ValidateConfig(cfg); err == nil {
		t.Fatal("unbounded trait allowlist accepted")
	}
}

func TestProfileTraitsAndOptionalClaims(t *testing.T) {
	p := newFixture(t)
	hash := sha256.Sum256([]byte("provider-access-token"))
	atHash := base64.RawURLEncoding.EncodeToString(hash[:sha256.Size/2])
	p.claims = map[string]any{
		"preferred_username": "operator", "name": "Jörg Example", "given_name": "Jörg",
		"family_name": "Example", "nickname": "ops", "locale": "de-DE",
		"groups": []string{"123:CEP_ADMIN"}, "email_verified": true, "auth_time": 1234567890,
		"amr": []string{"pwd", "mfa"}, "sid": "session-1", "at_hash": atHash,
		"employee_id": json.Number("9007199254740993"), "unselected": "must not escape",
		"zitadel_subject": "must not replace the verified subject",
	}
	cfg := testConfig()
	cfg.TraitClaims = []string{"groups", "email_verified", "amr", "auth_time", "aud", "azp", "sid", "at_hash", "employee_id"}
	client := p.client(cfg, memory(t))
	result, err := client.Complete(context.Background(), p.begin(client))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"email": {"operator@example.com"}, "iss": {"https://idp.example"}, "sub": {"immutable-user-id"},
		"preferred_username": {"operator"}, "name": {"Jörg Example"}, "given_name": {"Jörg"},
		"family_name": {"Example"}, "nickname": {"ops"}, "locale": {"de-DE"},
		"groups": {"123:CEP_ADMIN"}, "email_verified": {"true"}, "auth_time": {"1234567890"},
		"amr": {"pwd", "mfa"}, "aud": {"teleport-client"}, "azp": {"teleport-client"},
		"sid": {"session-1"}, "at_hash": {atHash}, "employee_id": {"9007199254740993"},
	}
	if !reflect.DeepEqual(result.Identity.Traits, want) || p.userInfoCalls.Load() != 0 {
		t.Fatalf("unexpected traits or unnecessary UserInfo lookup: %+v", result.Identity.Traits)
	}
	if !slices.Equal(result.Identity.Roles, []string{"access"}) {
		t.Fatal("groups changed explicit project/organization role mapping")
	}
}

func TestTraitUserInfoPrecedence(t *testing.T) {
	p := newFixture(t)
	p.claims = map[string]any{"name": "Signed Name", "locale": json.RawMessage("null"), "groups": []string{"signed-group"}}
	p.userinfo = map[string]any{
		"name": "UserInfo Name", "given_name": "Jörg", "locale": "de-DE", "groups": []string{"userinfo-group"},
		"department": "operations", "email": "different@example.com", "sid": "untrusted-session",
		"amr": []string{"mfa"}, "client_id": "userinfo-client", "zitadel_subject": "other-user",
	}
	cfg := testConfig()
	cfg.TraitClaims = []string{"groups", "department", "sid", "amr", "client_id"}
	client := p.client(cfg, memory(t))
	result, err := client.Complete(context.Background(), p.begin(client))
	if err != nil {
		t.Fatal(err)
	}
	traits := result.Identity.Traits
	for key, value := range map[string][]string{"name": {"Signed Name"}, "given_name": {"Jörg"}, "groups": {"signed-group"}, "department": {"operations"}, "email": {"operator@example.com"}, "sub": {"immutable-user-id"}} {
		if !slices.Equal(traits[key], value) {
			t.Fatalf("incorrect claim source for %s", key)
		}
	}
	for _, name := range []string{"locale", "sid", "amr", "client_id", "zitadel_subject"} {
		if _, exists := traits[name]; exists {
			t.Fatalf("invalid, unselected, or token-only trait came from UserInfo: %s", name)
		}
	}
}

func TestOptionalTraitsFromUserInfo(t *testing.T) {
	for _, tt := range []struct {
		name   string
		info   map[string]any
		status int
		denied bool
	}{
		{name: "profile and groups", info: map[string]any{"name": "Alice", "groups": []string{"123:CEP_ADMIN"}}},
		{name: "missing optional traits"},
		{name: "optional lookup unavailable", status: 503},
		{name: "different subject", info: map[string]any{"sub": "someone-else", "groups": []string{"admin"}}, denied: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newFixture(t)
			p.userinfo, p.userInfoStatus = tt.info, tt.status
			cfg := testConfig()
			cfg.TraitClaims = []string{"groups"}
			client := p.client(cfg, memory(t))
			result, err := client.Complete(context.Background(), p.begin(client))
			if tt.denied {
				if result != nil || !errors.Is(err, ErrDenied) {
					t.Fatal("UserInfo subject mismatch accepted")
				}
				return
			}
			if err != nil || p.userInfoCalls.Load() != 1 {
				t.Fatalf("optional UserInfo lookup failed login: %v", err)
			}
			if tt.info != nil && !slices.Equal(result.Identity.Traits["groups"], []string{"123:CEP_ADMIN"}) {
				t.Fatal("groups were not imported from verified-subject UserInfo")
			}
			if tt.info == nil && result.Identity.Traits["groups"] != nil {
				t.Fatal("missing groups were fabricated")
			}
		})
	}
}

func TestTraitValueTypesAndBounds(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want []string
	}{
		{`"Zoë Example"`, []string{"Zoë Example"}},
		{`["operator", "123:ADMIN"]`, []string{"operator", "123:ADMIN"}},
		{`true`, []string{"true"}}, {`false`, []string{"false"}},
		{`9007199254740993`, []string{"9007199254740993"}}, {`1.25`, []string{"1.25"}},
		{`null`, nil}, {`""`, nil}, {`"  "`, nil}, {`"bad\nvalue"`, nil},
		{`[]`, nil}, {`["valid", null]`, nil}, {`["valid", 1]`, nil}, {`[["nested"]]`, nil},
		{`{"admin":{"456":"org.example"}}`, nil},
		{`"` + strings.Repeat("x", maxTraitValueBytes+1) + `"`, nil},
		{`[` + strings.Repeat(`"group",`, maxTraitValues) + `"last"]`, nil},
	} {
		got, ok := traitValues(json.RawMessage(tt.raw))
		if ok != (tt.want != nil) || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("unexpected trait conversion for %.80s: %+v", tt.raw, got)
		}
	}
	values := make([]string, 40)
	for i := range values {
		values[i] = strings.Repeat("x", maxTraitValueBytes)
	}
	raw, _ := json.Marshal(values)
	claims := map[string]json.RawMessage{"groups": raw, "name": json.RawMessage(`"Alice"`)}
	traits := identityTraits(claims, []string{"groups", "name"}, "https://idp.example", "subject", "email@example.com")
	if traits["groups"] != nil || !slices.Equal(traits["name"], []string{"Alice"}) {
		t.Fatal("oversized list was partially exported or discarded unrelated profile data")
	}
}

func TestImportedGroupsCannotGrantProjectAccess(t *testing.T) {
	for _, grants := range []map[string]map[string]string{
		{"unmapped": {"456": "org.example"}},
		{"operator": {"999": "other-org.example"}},
	} {
		p := newFixture(t)
		p.claims = map[string]any{projectRolesClaim("123"): grants, "groups": []string{"123:administrator", "editor"}}
		cfg := testConfig()
		cfg.TraitClaims = []string{"groups"}
		client := p.client(cfg, memory(t))
		result, err := client.Complete(context.Background(), p.begin(client))
		if result != nil || !errors.Is(err, ErrDenied) {
			t.Fatal("imported groups bypassed configured project/organization role mappings")
		}
	}
}
