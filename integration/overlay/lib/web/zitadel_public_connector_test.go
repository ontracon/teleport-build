// SPDX-License-Identifier: AGPL-3.0-or-later
package web

import (
	"encoding/json"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/gravitational/teleport/lib/web/ui"
	"github.com/stretchr/testify/require"
)

func TestZitadelConnectorWithoutSecret(t *testing.T) {
	for _, field := range []string{"", `"client_secret":"",`} {
		t.Run(field, func(t *testing.T) {
			data := []byte(`{
				"kind":"oidc","version":"v3",
				"metadata":{"name":"zitadel","labels":{"zitadel/project-id":"123","zitadel/organization-id":"456"}},
				"spec":{` + field + `
					"provider":"zitadel","issuer_url":"https://idp.example","client_id":"client",
					"redirect_url":"https://teleport.example/v1/webapi/oidc/callback",
					"pkce_mode":"enabled",
					"claims_to_roles":[{"claim":"urn:zitadel:iam:org:project:123:roles","value":"operator","roles":["lab-access"]}]
				}
			}`)
			connector, err := unmarshalZitadelConnector(data)
			require.NoError(t, err)
			require.Empty(t, connector.GetClientSecret())
			redacted := redactedZitadelConnector(connector)
			require.Empty(t, connector.GetClientSecret(), "redaction must not mutate a public connector")
			require.Equal(t, zitadelHiddenSecret, redacted.GetClientSecret())

			item, err := ui.NewResourceItem(redacted)
			require.NoError(t, err)
			roundTripJSON, err := yaml.YAMLToJSON([]byte(item.Content))
			require.NoError(t, err)
			var resource map[string]any
			require.NoError(t, json.Unmarshal(roundTripJSON, &resource))
			resource["spec"].(map[string]any)["client_secret"] = ""
			data, err = json.Marshal(resource)
			require.NoError(t, err)
			roundTrip, err := unmarshalZitadelConnector(data)
			require.NoError(t, err)
			require.Empty(t, roundTrip.GetClientSecret())
		})
	}
}
