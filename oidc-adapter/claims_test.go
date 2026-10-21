package zitadel

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRoleMapping(t *testing.T) {
	for _, input := range []string{
		`{"operator":{"456":"org.example"},"administrator":{"456":"org.example"}}`,
		`[{"administrator":{"456":"org.example"}},{"operator":{"456":"org.example"}}]`,
	} {
		roles, err := mapRoles(json.RawMessage(input), testConfig())
		if err != nil || !reflect.DeepEqual(roles, []string{"access", "editor"}) {
			t.Fatalf("mapping=%v error=%v", roles, err)
		}
	}
	for _, input := range []string{`null`, `{}`, `[]`, `{"operator":null}`, `{"operator":{"456":""}}`, `{"operator":["456"]}`, `{"operator":{"999":"other.example"}}`} {
		if roles, err := mapRoles(json.RawMessage(input), testConfig()); roles != nil || err == nil {
			t.Fatalf("invalid grants accepted: %s", input)
		}
	}
}

func TestConfigOwnsMappings(t *testing.T) {
	cfg := testConfig()
	validated, err := cfg.validated()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RoleMappings["operator"][0] = "editor"
	if validated.RoleMappings["operator"][0] != "access" {
		t.Fatal("configuration shares mutable policy")
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, name := range []string{"http issuer", "http redirect", "blank client secret", "empty mappings", "wildcard role", "missing organization", "injected project scope"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			switch name {
			case "http issuer":
				cfg.IssuerURL = "http://idp.example"
			case "http redirect":
				cfg.RedirectURL = "http://teleport.example"
			case "blank client secret":
				cfg.ClientSecret = " \t"
			case "empty mappings":
				cfg.RoleMappings = nil
			case "wildcard role":
				cfg.RoleMappings["*"] = []string{"editor"}
			case "missing organization":
				cfg.OrganizationID = ""
			case "injected project scope":
				cfg.ProjectID = "123:aud openid"
			}
			if _, err := cfg.validated(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
