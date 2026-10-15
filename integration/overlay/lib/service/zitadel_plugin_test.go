// SPDX-License-Identifier: AGPL-3.0-or-later
package service

import (
	"testing"

	"github.com/gravitational/teleport/entitlements"
	"github.com/gravitational/teleport/lib/modules"
	"github.com/gravitational/teleport/lib/modules/modulestest"
	"github.com/gravitational/teleport/lib/plugin"
	"github.com/stretchr/testify/require"
)

func TestZitadelPluginEnabledByDefault(t *testing.T) {
	t.Setenv("TELEPORT_ZITADEL_ENABLED", "")
	t.Setenv("TELEPORT_ZITADEL_CONFIG", "")
	base := modulestest.OSSModules()
	modulestest.SetTestModules(t, *base)

	registry := plugin.NewRegistry()
	require.NoError(t, registerZitadelPlugin(registry))
	require.True(t, registry.IsRegistered("zitadel-oidc"))
	active := modules.GetModules()
	require.True(t, active.Features().GetEntitlement(entitlements.OIDC).Enabled)
	require.Equal(t, modules.BuildOSS, active.BuildType())
	require.False(t, base.Features().GetEntitlement(entitlements.OIDC).Enabled)

	// Repeated startup and separate registries must not nest module wrappers.
	require.NoError(t, registerZitadelPlugin(registry))
	second := plugin.NewRegistry()
	require.NoError(t, registerZitadelPlugin(second))
	require.True(t, second.IsRegistered("zitadel-oidc"))
	require.Same(t, active, modules.GetModules())
}
