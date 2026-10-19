// SPDX-License-Identifier: AGPL-3.0-or-later
package ui

import (
	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
)

// zitadelUserDisplayName uses the provider display label persisted by our adapter.
// It is presentation data, independent of hashed or claim-based account names.
func zitadelUserDisplayName(user types.User) string {
	ref := user.GetCreatedBy().Connector
	identities := user.GetOIDCIdentities()
	if ref == nil || ref.Type != constants.OIDC || len(identities) != 1 {
		return ""
	}
	identity := identities[0]
	if identity.ConnectorID != ref.ID || identity.UserID == "" || identity.UserID != ref.Identity {
		return ""
	}
	traits := user.GetTraits()
	if len(traits["zitadel_issuer"]) != 1 || traits["zitadel_issuer"][0] == "" ||
		len(traits["zitadel_subject"]) != 1 || traits["zitadel_subject"][0] != identity.UserID {
		return ""
	}
	return identity.Username
}
