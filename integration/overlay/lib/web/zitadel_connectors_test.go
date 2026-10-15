// SPDX-License-Identifier: AGPL-3.0-or-later
package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth/authclient"
	"github.com/gravitational/teleport/lib/web/ui"
	"github.com/stretchr/testify/require"
)

type zitadelReadOnlyPreferenceClient struct {
	authclient.ClientI
	t          *testing.T
	preference types.AuthPreference
}

func (c zitadelReadOnlyPreferenceClient) GetAuthPreference(context.Context) (types.AuthPreference, error) {
	return c.preference, nil
}
func (c zitadelReadOnlyPreferenceClient) UpsertAuthPreference(context.Context, types.AuthPreference) (types.AuthPreference, error) {
	c.t.Fatal("a connector listing must never change authentication preferences")
	return nil, nil
}

func TestZitadelConnectorListingPreservesDefault(t *testing.T) {
	pref := &types.AuthPreferenceV2{Spec: types.AuthPreferenceSpecV2{Type: "oidc", ConnectorName: "zitadel"}}
	client := zitadelReadOnlyPreferenceClient{t: t, preference: pref}
	name, kind, err := configuredAuthDefault(context.Background(), client)
	require.NoError(t, err)
	require.Equal(t, "zitadel", name)
	require.Equal(t, "oidc", kind)
	require.Equal(t, "oidc", pref.GetType())
}

type zitadelAuthoritativePreferenceClient struct {
	zitadelReadOnlyPreferenceClient
	current types.AuthPreference
}

func (c zitadelAuthoritativePreferenceClient) GetZitadelAuthPreference(context.Context) (types.AuthPreference, error) {
	return c.current, nil
}

func TestZitadelDefaultUsesAuthoritativePreference(t *testing.T) {
	stale := &types.AuthPreferenceV2{Spec: types.AuthPreferenceSpecV2{Type: "local"}}
	current := &types.AuthPreferenceV2{Spec: types.AuthPreferenceSpecV2{Type: "oidc", ConnectorName: "zitadel"}}
	client := zitadelAuthoritativePreferenceClient{zitadelReadOnlyPreferenceClient: zitadelReadOnlyPreferenceClient{t: t, preference: stale}, current: current}
	name, kind, err := configuredAuthDefault(context.Background(), client)
	require.NoError(t, err)
	require.Equal(t, "zitadel", name)
	require.Equal(t, "oidc", kind)
}

func TestZitadelConnectorParsingAndSecretRedaction(t *testing.T) {
	resource := map[string]any{
		"kind": "oidc", "version": "v3",
		"metadata": map[string]any{"name": "zitadel", "revision": "revision-1", "labels": map[string]string{"zitadel/project-id": "123", "zitadel/organization-id": "456"}},
		"spec": map[string]any{
			"provider": "zitadel", "issuer_url": "https://idp.example", "client_id": "client", "client_secret": "SECRET-private",
			"redirect_url":    "https://teleport.example/v1/webapi/oidc/callback",
			"claims_to_roles": []any{map[string]any{"claim": "urn:zitadel:iam:org:project:123:roles", "value": "operator", "roles": []string{"lab-access"}}},
		},
	}
	data, err := json.Marshal(resource)
	require.NoError(t, err)
	connector, err := unmarshalZitadelConnector(data)
	require.NoError(t, err)
	item, err := ui.NewResourceItem(redactedZitadelConnector(connector))
	require.NoError(t, err)
	require.NotContains(t, item.Content, "SECRET")
	require.Contains(t, item.Content, zitadelHiddenSecret)
	require.Equal(t, "SECRET-private", connector.GetClientSecret(), "redaction must not mutate the stored credential")
	resource["spec"].(map[string]any)["client_secert"] = "SECRET-unknown-field"
	data, _ = json.Marshal(resource)
	_, err = unmarshalZitadelConnector(data)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET")
}
