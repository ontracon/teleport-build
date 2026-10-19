// SPDX-License-Identifier: AGPL-3.0-or-later
package ui

import (
	"encoding/json"
	"testing"

	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
	"github.com/stretchr/testify/require"
)

func TestZitadelDisplayNamePreservesIdentity(t *testing.T) {
	user := &types.UserV2{
		Metadata: types.Metadata{Name: "zitadel-stable-identity"},
		Spec: types.UserSpecV2{
			OIDCIdentities: []types.ExternalIdentity{{ConnectorID: "zitadel", UserID: "subject", Username: "person@example.com"}},
			CreatedBy:      types.CreatedBy{Connector: &types.ConnectorRef{Type: constants.OIDC, ID: "zitadel", Identity: "subject"}},
		},
	}
	user.SetTraits(map[string][]string{
		"email": {"verified@example.com"}, "zitadel_issuer": {"https://idp.example"}, "zitadel_subject": {"subject"},
	})
	for _, name := range []string{"zitadel-stable-identity", "alice", "alice@example.com"} {
		user.SetName(name)
		for _, label := range []string{"person@example.com", "updated@example.com", "preferred-user", "Zoë Example", "Nick"} {
			user.Spec.OIDCIdentities[0].Username = label
			context, err := NewUserContext(user, nil, proto.Features{}, false, false)
			require.NoError(t, err)
			require.Equal(t, name, context.Name)
			require.Equal(t, label, context.DisplayName)
			require.Equal(t, []string{"verified@example.com"}, user.GetTraits()["email"])
			data, err := json.Marshal(context)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(data, &fields))
			require.Equal(t, name, fields["userName"])
			require.Equal(t, label, fields["displayName"])
		}
	}
}

func TestZitadelDisplayNameRequiresMatchingIdentity(t *testing.T) {
	for _, name := range []string{"local user", "other connector", "other subject", "missing ownership", "missing subject", "multiple identities", "other OIDC provider", "mismatched subject trait"} {
		t.Run(name, func(t *testing.T) {
			user := &types.UserV2{
				Metadata: types.Metadata{Name: "zitadel-stable-identity"},
				Spec: types.UserSpecV2{
					Traits: map[string][]string{
						"email": {"untrusted@example.com"}, "zitadel_issuer": {"https://idp.example"}, "zitadel_subject": {"subject"},
					},
					OIDCIdentities: []types.ExternalIdentity{{ConnectorID: "zitadel", UserID: "subject", Username: "person@example.com"}},
					CreatedBy:      types.CreatedBy{Connector: &types.ConnectorRef{Type: constants.OIDC, ID: "zitadel", Identity: "subject"}},
				},
			}
			switch name {
			case "local user":
				user.Metadata.Name = "local"
				user.Spec.CreatedBy.Connector = nil
				user.Spec.OIDCIdentities = nil
			case "other connector":
				user.Spec.CreatedBy.Connector.ID = "other"
			case "other subject":
				user.Spec.CreatedBy.Connector.Identity = "other"
			case "missing ownership":
				user.Spec.CreatedBy.Connector = nil
			case "missing subject":
				user.Spec.OIDCIdentities[0].UserID = ""
			case "multiple identities":
				user.Spec.OIDCIdentities = append(user.Spec.OIDCIdentities, user.Spec.OIDCIdentities[0])
			case "other OIDC provider":
				delete(user.Spec.Traits, "zitadel_issuer")
			case "mismatched subject trait":
				user.Spec.Traits["zitadel_subject"] = []string{"someone-else"}
			}
			context, err := NewUserContext(user, nil, proto.Features{}, false, false)
			require.NoError(t, err)
			require.Equal(t, user.GetName(), context.Name)
			require.Empty(t, context.DisplayName)
		})
	}
}
