// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/backend/lite"
	"github.com/gravitational/teleport/lib/backend/memory"
	zitadel "github.com/gravitational/teleport/lib/zitadeloidc"
)

func TestZitadelAdapterWithoutBootstrap(t *testing.T) {
	t.Setenv("TELEPORT_ZITADEL_CONFIG", "")
	t.Setenv("TELEPORT_ZITADEL_SECRET_FILE", "")
	bk, err := memory.New(memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bk.Close() })
	server := &Server{bk: bk}
	if err := server.ConfigureZitadelAdapter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := server.oidcAuthService.(*zitadelService); !ok {
		t.Fatal("Zitadel OIDC service was not installed without bootstrap settings")
	}
}

func TestZitadelBackendSingleUse(t *testing.T) {
	bk, err := memory.New(memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bk.Close() })
	testZitadelBackendConsumption(t, bk)
}

func TestZitadelSQLiteSingleUse(t *testing.T) {
	bk, err := lite.NewWithConfig(context.Background(), lite.Config{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bk.Close() })
	testZitadelBackendConsumption(t, bk)
}

func testZitadelBackendConsumption(t *testing.T, bk backend.Backend) {
	t.Helper()
	protected := backend.NewSanitizer(bk)
	writer, err := zitadel.NewBackendStore(zitadelBackend{backend: protected})
	if err != nil {
		t.Fatal(err)
	}
	tx := zitadel.Transaction{State: "unique-state", ExpiresAt: time.Now().Add(time.Minute), Request: []byte("original-request")}
	if err := writer.Put(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader, _ := zitadel.NewBackendStore(zitadelBackend{backend: protected})
			result, err := reader.Consume(context.Background(), tx.State)
			if err == nil {
				if string(result.Request) != "original-request" {
					t.Error("original request changed")
				}
				count.Add(1)
			} else if !errors.Is(err, zitadel.ErrTransaction) {
				t.Errorf("unexpected backend error: %v", err)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("consumed %d times", count.Load())
	}
	if err := writer.Put(context.Background(), tx); !errors.Is(err, zitadel.ErrTransaction) {
		t.Fatal("consumed state could be recreated")
	}
}

func TestZitadelIdentityNames(t *testing.T) {
	identity := zitadel.Identity{Issuer: "https://idp.example", Subject: "user-1", Email: "old@example.com"}
	name := zitadelUsername("zitadel", identity)
	identity.Email = "new@example.com"
	if name != zitadelUsername("zitadel", identity) {
		t.Fatal("email change changed identity key")
	}
	identity.DisplayName = "New Display Name"
	if name != zitadelUsername("zitadel", identity) {
		t.Fatal("display name change changed identity key")
	}
	if name == zitadelUsername("other-connector", identity) {
		t.Fatal("connector identity collision")
	}
	identity.Issuer = "https://another-idp.example"
	if name == zitadelUsername("zitadel", identity) {
		t.Fatal("issuer identity collision")
	}
}

func TestZitadelUserOwnership(t *testing.T) {
	for _, scenario := range []string{"same user", "local user", "different connector", "different subject", "multiple identities"} {
		t.Run(scenario, func(t *testing.T) {
			user := &types.UserV2{Spec: types.UserSpecV2{
				CreatedBy:      types.CreatedBy{Connector: &types.ConnectorRef{Type: constants.OIDC, ID: "zitadel"}},
				OIDCIdentities: []types.ExternalIdentity{{ConnectorID: "zitadel", UserID: "subject-1"}},
			}}
			switch scenario {
			case "local user":
				user.Spec.CreatedBy.Connector = nil
			case "different connector":
				user.Spec.CreatedBy.Connector.ID = "another"
			case "different subject":
				user.Spec.OIDCIdentities[0].UserID = "another"
			case "multiple identities":
				user.Spec.OIDCIdentities = append(user.Spec.OIDCIdentities, user.Spec.OIDCIdentities[0])
			}
			if zitadelOwnsUser(user, "zitadel", "subject-1") != (scenario == "same user") {
				t.Fatal("unexpected identity ownership result")
			}
		})
	}
}
