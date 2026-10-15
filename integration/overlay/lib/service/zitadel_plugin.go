// SPDX-License-Identifier: AGPL-3.0-or-later
package service

import (
	"context"
	"crypto/tls"
	"maps"

	"github.com/gravitational/teleport/entitlements"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/modules"
	"github.com/gravitational/teleport/lib/plugin"
	"github.com/gravitational/teleport/lib/web"
	"github.com/gravitational/trace"
)

// The only upstream startup change calls this function after constructing the
// plugin registry. All login routes and the Auth service use extension hooks.
// Zitadel is always available in this custom build; native connectors configure it.
func registerZitadelPlugin(registry plugin.Registry) error {
	if registry.IsRegistered("zitadel-oidc") {
		return nil
	}
	base := modules.GetModules()
	if _, already := base.(*zitadelModules); !already {
		modules.SetModules(&zitadelModules{Modules: base})
	}
	return registry.Add(zitadelPlugin{})
}

type zitadelModules struct{ modules.Modules }

func (m *zitadelModules) Features() modules.Features {
	features := m.Modules.Features()
	features.Entitlements = maps.Clone(features.Entitlements)
	if features.Entitlements == nil {
		features.Entitlements = make(map[entitlements.EntitlementKind]modules.EntitlementInfo)
	}
	features.Entitlements[entitlements.OIDC] = modules.EntitlementInfo{Enabled: true}
	return features
}

type zitadelPlugin struct{}

func (zitadelPlugin) GetName() string { return "zitadel-oidc" }
func (zitadelPlugin) RegisterProxyWebHandlers(handler interface{}) error {
	h, ok := handler.(*web.Handler)
	if !ok {
		return trace.BadParameter("unexpected Zitadel Proxy handler")
	}
	h.RegisterZitadelHandlers()
	return nil
}
func (zitadelPlugin) RegisterAuthWebHandlers(handler interface{}) error {
	h, ok := handler.(*auth.APIServer)
	if !ok {
		return trace.BadParameter("unexpected Zitadel Auth handler")
	}
	h.RegisterZitadelHandlers()
	return nil
}
func (zitadelPlugin) RegisterAuthServices(ctx context.Context, server any, _ func() (*tls.Certificate, error)) error {
	g, ok := server.(*auth.GRPCServer)
	if !ok {
		return trace.BadParameter("unexpected Zitadel Auth server")
	}
	return g.AuthServer.ConfigureZitadelAdapter(ctx)
}
