// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"testing"
	"time"

	"github.com/gravitational/teleport/api/types"
	"github.com/stretchr/testify/require"
)

func testNativeZitadelConnector(t *testing.T) *types.OIDCConnectorV3 {
	t.Helper()
	connector := &types.OIDCConnectorV3{
		Kind: types.KindOIDC, Version: types.V3,
		Metadata: types.Metadata{Name: "zitadel", Revision: "revision-1", Labels: map[string]string{
			ZitadelProjectLabel: "123", ZitadelOrganizationLabel: "456", ZitadelTTLLabel: "5m",
		}},
		Spec: types.OIDCConnectorSpecV3{
			IssuerURL: "https://idp.example", ClientID: "teleport-client", ClientSecret: "SECRET-test-client",
			Provider: "zitadel", RedirectURLs: []string{"https://teleport.example/v1/webapi/oidc/callback"},
			ClaimsToRoles: []types.ClaimMapping{{Claim: "urn:zitadel:iam:org:project:123:roles", Value: "operator", Roles: []string{"lab-access"}}},
		},
	}
	require.NoError(t, connector.CheckAndSetDefaults())
	return connector
}

func TestZitadelNativePolicyWithoutSecret(t *testing.T) {
	c := testNativeZitadelConnector(t)
	c.SetClientSecret("")
	require.NoError(t, c.CheckAndSetDefaults())
	cfg, err := ZitadelConfigFromConnector(c)
	require.NoError(t, err)
	require.Empty(t, cfg.ClientSecret)

	c.Spec.PKCEMode = "disabled"
	_, err = ZitadelConfigFromConnector(c)
	require.Error(t, err, "public clients must still use PKCE")

	c.SetProvider("")
	require.Error(t, c.CheckAndSetDefaults(), "other OIDC providers still require a secret")
}

func TestZitadelNativePolicy(t *testing.T) {
	c := testNativeZitadelConnector(t)
	c.Spec.MaxAge = &types.MaxAge{Value: types.NewDuration(10 * time.Minute)}
	c.Metadata.Labels[ZitadelAMRLabel] = `["otp"]`
	c.Metadata.Labels[ZitadelDisplayNameLabel] = "preferred_username"
	c.Spec.UsernameClaim = "sub"
	c.Metadata.Labels[ZitadelTraitClaimsLabel] = `["groups","amr"]`
	cfg, err := ZitadelConfigFromConnector(c)
	require.NoError(t, err)
	require.Equal(t, "123", cfg.ProjectID)
	require.Equal(t, "456", cfg.OrganizationID)
	require.Equal(t, 5*time.Minute, cfg.TransactionTTL)
	require.Equal(t, 10*time.Minute, cfg.MaxAuthenticationAge)
	require.Equal(t, []string{"otp"}, cfg.RequiredAMR)
	require.Equal(t, "preferred_username", cfg.DisplayNameClaim)
	require.Equal(t, "sub", cfg.UsernameClaim)
	require.Equal(t, []string{"amr", "groups"}, cfg.TraitClaims)
	require.Equal(t, []string{"lab-access"}, cfg.RoleMappings["operator"])
	require.Len(t, cfg.PolicyRevision, 64)
	c.Spec.ClaimsToRoles[0].Roles[0] = "editor"
	require.Equal(t, []string{"lab-access"}, cfg.RoleMappings["operator"])
}

func TestZitadelNativeDisplayNamePolicy(t *testing.T) {
	c := testNativeZitadelConnector(t)
	cfg, err := ZitadelConfigFromConnector(c)
	require.NoError(t, err)
	require.Equal(t, "email", cfg.DisplayNameClaim)
	require.Empty(t, cfg.UsernameClaim)
	c.Metadata.Labels[ZitadelDisplayNameLabel] = "name"
	cfg, err = ZitadelConfigFromConnector(c)
	require.NoError(t, err)
	require.Equal(t, "name", cfg.DisplayNameClaim)
	c.Metadata.Labels[ZitadelDisplayNameLabel] = "SECRET-invalid claim"
	_, err = ZitadelConfigFromConnector(c)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET")
}

func TestZitadelNativePolicyRejectsUnsupportedConfiguration(t *testing.T) {
	for _, name := range []string{"other provider", "wrong project claim", "generic role claim", "missing organization", "unknown label", "unverified email", "invalid username claim", "disabled PKCE", "extra scope", "wildcard role", "duplicate mapping", "invalid AMR", "invalid TTL", "invalid trait JSON", "null trait list", "reserved trait", "invalid trait name"} {
		t.Run(name, func(t *testing.T) {
			c := testNativeZitadelConnector(t)
			switch name {
			case "other provider":
				c.Spec.Provider = "other"
			case "wrong project claim":
				c.Spec.ClaimsToRoles[0].Claim = "urn:zitadel:iam:org:project:999:roles"
			case "generic role claim":
				c.Spec.ClaimsToRoles[0].Claim = "urn:zitadel:iam:org:project:roles"
			case "missing organization":
				delete(c.Metadata.Labels, ZitadelOrganizationLabel)
			case "unknown label":
				c.Metadata.Labels["zitadel/organization-id-typo"] = "456"
			case "unverified email":
				c.Spec.AllowUnverifiedEmail = true
			case "invalid username claim":
				c.Spec.UsernameClaim = "SECRET-invalid claim"
			case "disabled PKCE":
				c.Spec.PKCEMode = "disabled"
			case "extra scope":
				c.Spec.Scope = []string{"unhandled-scope"}
			case "wildcard role":
				c.Spec.ClaimsToRoles[0].Value = "*"
			case "duplicate mapping":
				c.Spec.ClaimsToRoles = append(c.Spec.ClaimsToRoles, c.Spec.ClaimsToRoles[0])
			case "invalid AMR":
				c.Metadata.Labels[ZitadelAMRLabel] = "SECRET-invalid-json"
			case "invalid TTL":
				c.Metadata.Labels[ZitadelTTLLabel] = "SECRET-not-a-duration"
			case "invalid trait JSON":
				c.Metadata.Labels[ZitadelTraitClaimsLabel] = "SECRET-invalid-json"
			case "null trait list":
				c.Metadata.Labels[ZitadelTraitClaimsLabel] = "null"
			case "reserved trait":
				c.Metadata.Labels[ZitadelTraitClaimsLabel] = `["zitadel_subject"]`
			case "invalid trait name":
				c.Metadata.Labels[ZitadelTraitClaimsLabel] = `["SECRET-invalid claim"]`
			}
			_, err := ZitadelConfigFromConnector(c)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "SECRET")
		})
	}
}

func TestZitadelNativeRevisionsBindEveryPolicyChange(t *testing.T) {
	for _, name := range []string{"revision", "secret", "organization", "project", "roles", "name", "display claim", "username claim", "trait claims"} {
		t.Run(name, func(t *testing.T) {
			c := testNativeZitadelConnector(t)
			original, err := ZitadelConfigFromConnector(c)
			require.NoError(t, err)
			switch name {
			case "revision":
				c.SetRevision("revision-2")
			case "secret":
				c.Spec.ClientSecret = "rotated-secret"
			case "organization":
				c.Metadata.Labels[ZitadelOrganizationLabel] = "789"
			case "project":
				c.Metadata.Labels[ZitadelProjectLabel] = "999"
				c.Spec.ClaimsToRoles[0].Claim = "urn:zitadel:iam:org:project:999:roles"
			case "roles":
				c.Spec.ClaimsToRoles[0].Roles = []string{"editor"}
			case "name":
				c.SetName("another-connector")
			case "display claim":
				c.Metadata.Labels[ZitadelDisplayNameLabel] = "name"
			case "username claim":
				c.Spec.UsernameClaim = "preferred_username"
			case "trait claims":
				c.Metadata.Labels[ZitadelTraitClaimsLabel] = `["groups"]`
			}
			updated, err := ZitadelConfigFromConnector(c)
			require.NoError(t, err)
			require.NotEqual(t, original.PolicyRevision, updated.PolicyRevision)
		})
	}
}
