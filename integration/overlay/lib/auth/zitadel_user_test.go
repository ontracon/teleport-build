// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/backend/memory"
	"github.com/gravitational/teleport/lib/services/local"
	zitadel "github.com/gravitational/teleport/lib/zitadeloidc"
	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"
)

func newZitadelUserStore(t *testing.T) *local.IdentityService {
	t.Helper()
	bk, err := memory.New(memory.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { bk.Close() })
	store, err := local.NewIdentityService(backend.NewSanitizer(bk))
	require.NoError(t, err)
	return store
}

func testZitadelUser(connector string, identity zitadel.Identity) *types.UserV2 {
	expires := time.Now().UTC().Add(time.Hour)
	label := identity.DisplayName
	if label == "" {
		label = identity.Email
	}
	if label == "" {
		label = zitadelUsername(connector, identity)
	}
	return &types.UserV2{
		Metadata: types.Metadata{Name: zitadelUsername(connector, identity), Expires: &expires},
		Spec: types.UserSpecV2{
			Roles:          []string{"access"},
			Traits:         zitadelTraits(identity),
			OIDCIdentities: []types.ExternalIdentity{{ConnectorID: connector, UserID: identity.Subject, Username: label}},
			CreatedBy: types.CreatedBy{Time: time.Now().UTC(),
				Connector: &types.ConnectorRef{Type: constants.OIDC, ID: connector, Identity: identity.Subject}},
		},
	}
}

func TestZitadelClaimNamedUserRefresh(t *testing.T) {
	ctx := context.Background()
	store := newZitadelUserStore(t)
	identity := zitadel.Identity{Issuer: "https://idp.example", Subject: "subject-1", Username: "alice", Email: "alice@example.com", DisplayName: "Alice Example"}
	identity.Traits = map[string][]string{"name": {"Alice Example"}, "groups": {"123:operator"}}
	require.NoError(t, upsertZitadelUser(ctx, store, testZitadelUser("zitadel", identity), "zitadel", identity))
	before, err := store.GetUser(ctx, "alice", false)
	require.NoError(t, err)
	require.Equal(t, []string{"123:operator"}, before.GetTraits()["groups"])
	identity.Email, identity.DisplayName = "updated@example.com", "Updated Name"
	identity.Traits = map[string][]string{"name": {"Updated Name"}}
	updated := testZitadelUser("zitadel", identity)
	updated.SetRoles([]string{"access", "editor"})
	require.NoError(t, upsertZitadelUser(ctx, store, updated, "zitadel", identity))
	after, err := store.GetUser(ctx, "alice", false)
	require.NoError(t, err)
	require.Equal(t, "alice", after.GetName())
	require.Equal(t, before.GetCreatedBy(), after.GetCreatedBy())
	require.NotEqual(t, before.GetRevision(), after.GetRevision())
	require.Equal(t, []string{"access", "editor"}, after.GetRoles())
	require.Equal(t, []string{identity.Email}, after.GetTraits()["email"])
	require.Equal(t, []string{"Updated Name"}, after.GetTraits()["name"])
	require.NotContains(t, after.GetTraits(), "groups", "absent claims must not leave stale membership on refresh")
	require.Equal(t, []string{identity.Issuer}, after.GetTraits()["iss"])
	require.Equal(t, []string{identity.Subject}, after.GetTraits()["sub"])
	require.Equal(t, identity.DisplayName, after.GetOIDCIdentities()[0].Username)
	require.True(t, zitadelOwnsUser(after, "zitadel", identity))
}

func TestZitadelUsernamePolicyDoesNotRenameExistingUsers(t *testing.T) {
	ctx := context.Background()
	store := newZitadelUserStore(t)
	identity := zitadel.Identity{Issuer: "https://idp.example", Subject: "subject-1", Email: "alice@example.com"}
	hashed := testZitadelUser("zitadel", identity)
	require.NoError(t, upsertZitadelUser(ctx, store, hashed, "zitadel", identity))
	original, err := store.GetUser(ctx, hashed.GetName(), false)
	require.NoError(t, err)
	for _, name := range []string{"alice", "alice.smith"} {
		identity.Username = name
		require.NoError(t, upsertZitadelUser(ctx, store, testZitadelUser("zitadel", identity), "zitadel", identity))
	}
	retained, err := store.GetUser(ctx, hashed.GetName(), false)
	require.NoError(t, err)
	require.Equal(t, original.GetRevision(), retained.GetRevision())
	require.Equal(t, original.Expiry(), retained.Expiry())
	users, err := store.GetUsers(ctx, false)
	require.NoError(t, err)
	require.Len(t, users, 3)
}

func TestZitadelClaimNamedUserRejectsCollisions(t *testing.T) {
	identity := zitadel.Identity{Issuer: "https://idp.example", Subject: "subject-1", Username: "alice", Email: "alice@example.com"}
	for _, scenario := range []string{"local account", "different issuer", "different subject", "different connector", "missing binding"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store := newZitadelUserStore(t)
			existing := testZitadelUser("zitadel", identity)
			switch scenario {
			case "local account":
				existing.Spec.CreatedBy = types.CreatedBy{}
				existing.Spec.OIDCIdentities = nil
			case "different issuer":
				existing.Spec.Traits["zitadel_issuer"] = []string{"https://other.example"}
			case "different subject":
				existing.Spec.OIDCIdentities[0].UserID = "someone-else"
				existing.Spec.CreatedBy.Connector.Identity = "someone-else"
				existing.Spec.Traits["zitadel_subject"] = []string{"someone-else"}
			case "different connector":
				existing.Spec.CreatedBy.Connector.ID = "other"
				existing.Spec.OIDCIdentities[0].ConnectorID = "other"
			case "missing binding":
				delete(existing.Spec.Traits, "zitadel_issuer")
			}
			_, err := store.CreateUser(ctx, existing)
			require.NoError(t, err)
			before, err := store.GetUser(ctx, "alice", false)
			require.NoError(t, err)
			err = upsertZitadelUser(ctx, store, testZitadelUser("zitadel", identity), "zitadel", identity)
			require.True(t, trace.IsAccessDenied(err), "expected a collision rejection: %v", err)
			after, err := store.GetUser(ctx, "alice", false)
			require.NoError(t, err)
			require.Equal(t, before, after, "a rejected login changed the existing account")
		})
	}
}

func TestZitadelConcurrentClaimNameCannotReplaceAnotherSubject(t *testing.T) {
	ctx := context.Background()
	store := newZitadelUserStore(t)
	results := make(chan error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, subject := range []string{"subject-1", "subject-2"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			identity := zitadel.Identity{Issuer: "https://idp.example", Subject: subject, Username: "alice"}
			results <- upsertZitadelUser(ctx, store, testZitadelUser("zitadel", identity), "zitadel", identity)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.True(t, trace.IsAccessDenied(err) || trace.IsAlreadyExists(err), "unexpected race result: %v", err)
		}
	}
	require.Equal(t, 1, successes)
}

type zitadelRacingUserWriter struct {
	zitadelUserWriter
	beforeUpdate func()
}

func (w zitadelRacingUserWriter) UpdateUser(ctx context.Context, user types.User) (types.User, error) {
	w.beforeUpdate()
	return w.zitadelUserWriter.UpdateUser(ctx, user)
}

func TestZitadelUserUpdateChecksRevisionAfterOwnership(t *testing.T) {
	ctx := context.Background()
	store := newZitadelUserStore(t)
	identity := zitadel.Identity{Issuer: "https://idp.example", Subject: "subject-1", Username: "alice"}
	require.NoError(t, upsertZitadelUser(ctx, store, testZitadelUser("zitadel", identity), "zitadel", identity))
	other := identity
	other.Subject = "subject-2"
	writer := zitadelRacingUserWriter{zitadelUserWriter: store, beforeUpdate: func() {
		current, err := store.GetUser(ctx, "alice", false)
		require.NoError(t, err)
		replacement := testZitadelUser("zitadel", other)
		replacement.SetRevision(current.GetRevision())
		_, err = store.UpdateUser(ctx, replacement)
		require.NoError(t, err)
	}}
	err := upsertZitadelUser(ctx, writer, testZitadelUser("zitadel", identity), "zitadel", identity)
	require.True(t, trace.IsCompareFailed(err), "expected revision conflict: %v", err)
	stored, err := store.GetUser(ctx, "alice", false)
	require.NoError(t, err)
	require.True(t, zitadelOwnsUser(stored, "zitadel", other))
}
