// SPDX-License-Identifier: AGPL-3.0-or-later
package ui

import (
	"strings"

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
)

// zitadelUserDisplayName uses the provider display label persisted by our adapter.
// It is presentation data; the internal username remains the identity key.
func zitadelUserDisplayName(user types.User) string {
	ref := user.GetCreatedBy().Connector
	identities := user.GetOIDCIdentities()
	if !strings.HasPrefix(user.GetName(), "zitadel-") || ref == nil || ref.Type != constants.OIDC || len(identities) != 1 {
		return ""
	}
	identity := identities[0]
	if identity.ConnectorID != ref.ID || identity.UserID == "" || identity.UserID != ref.Identity {
		return ""
	}
	return identity.Username
}
