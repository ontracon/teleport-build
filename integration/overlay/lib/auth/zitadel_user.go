// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"context"

	"github.com/gravitational/teleport/api/types"
	zitadel "github.com/gravitational/teleport/lib/zitadeloidc"
	"github.com/gravitational/trace"
)

type zitadelUserWriter interface {
	GetUser(context.Context, string, bool) (types.User, error)
	CreateUser(context.Context, types.User) (types.User, error)
	UpdateUser(context.Context, types.User) (types.User, error)
}

// upsertZitadelUser never links accounts by a mutable username alone. Create
// and revision-checked update keep concurrent logins from replacing another
// identity's account. Changing naming policy does not rename existing users.
func upsertZitadelUser(ctx context.Context, users zitadelUserWriter, user types.User, connector string, identity zitadel.Identity) error {
	if !zitadelOwnsUser(user, connector, identity) {
		return trace.AccessDenied("Zitadel user is missing its verified identity binding")
	}
	existing, err := users.GetUser(ctx, user.GetName(), false)
	if err != nil && !trace.IsNotFound(err) {
		return trace.Wrap(err)
	}
	if existing != nil {
		if !zitadelOwnsUser(existing, connector, identity) {
			return trace.AccessDenied("Zitadel user name belongs to another identity")
		}
		user.SetCreatedBy(existing.GetCreatedBy())
		user.SetRevision(existing.GetRevision())
		_, err = users.UpdateUser(ctx, user)
	} else {
		_, err = users.CreateUser(ctx, user)
	}
	return trace.Wrap(err)
}
