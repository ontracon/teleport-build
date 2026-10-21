// SPDX-License-Identifier: AGPL-3.0-or-later
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"

	"github.com/gravitational/teleport/api/constants"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/authclient"
	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/web/ui"
	"github.com/gravitational/trace"
	"github.com/julienschmidt/httprouter"
)

const zitadelHiddenSecret = "<existing-secret>"

func authoritativeAuthPreference(ctx context.Context, client authclient.ClientI) (types.AuthPreference, error) {
	if reader, ok := client.(interface {
		GetZitadelAuthPreference(context.Context) (types.AuthPreference, error)
	}); ok {
		preference, err := reader.GetZitadelAuthPreference(ctx)
		if !trace.IsNotFound(err) && !trace.IsNotImplemented(err) {
			return preference, err
		}
	}
	return client.GetAuthPreference(ctx)
}

func configuredAuthDefault(ctx context.Context, client authclient.ClientI) (string, string, error) {
	preference, err := authoritativeAuthPreference(ctx, client)
	if err != nil {
		return "", "", trace.Wrap(err)
	}
	if preference.GetType() == constants.Local {
		return "", constants.Local, nil
	}
	return preference.GetConnectorName(), preference.GetType(), nil
}

func redactedZitadelConnector(connector types.OIDCConnector) types.OIDCConnector {
	// WithoutSecrets may return the original object when its secret is empty.
	copy := *connector.WithoutSecrets().(*types.OIDCConnectorV3)
	copy.SetClientSecret(zitadelHiddenSecret)
	return &copy
}

func supportedAuthConnectors(ctx context.Context, client authclient.ClientI) ([]ui.ResourceItem, error) {
	github, err := client.GetGithubConnectors(ctx, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	items, err := ui.NewGithubConnectors(github)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	oidc, err := client.GetOIDCConnectors(ctx, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	for _, connector := range oidc {
		if connector.GetProvider() != "zitadel" {
			continue
		}
		item, err := ui.NewResourceItem(redactedZitadelConnector(connector))
		if err != nil {
			return nil, trace.Wrap(err)
		}
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (h *Handler) listZitadelAuthConnectors(w http.ResponseWriter, r *http.Request, p httprouter.Params, session *SessionContext) (interface{}, error) {
	client, err := session.GetClient()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	items, err := supportedAuthConnectors(r.Context(), client)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	name, kind, err := configuredAuthDefault(r.Context(), client)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &ui.ListAuthConnectorsResponse{Connectors: items, DefaultConnectorName: name, DefaultConnectorType: kind}, nil
}

func (h *Handler) getZitadelConnector(w http.ResponseWriter, r *http.Request, p httprouter.Params, session *SessionContext) (interface{}, error) {
	client, err := session.GetClient()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	connector, err := client.GetOIDCConnector(r.Context(), p.ByName("name"), false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if connector.GetProvider() != "zitadel" {
		return nil, trace.NotFound("Zitadel connector not found")
	}
	return ui.NewResourceItem(redactedZitadelConnector(connector))
}

func unmarshalZitadelConnector(data []byte, _ ...services.MarshalOption) (types.OIDCConnector, error) {
	if len(data) > 64<<10 {
		return nil, trace.BadParameter("Zitadel connector configuration exceeds 64 KiB")
	}
	var connector types.OIDCConnectorV3
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&connector) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, trace.BadParameter("invalid Zitadel connector YAML or unknown field")
	}
	if connector.Kind != types.KindOIDC || connector.Version != types.V3 {
		return nil, trace.BadParameter("Zitadel connectors require kind oidc and version v3")
	}
	if err := connector.CheckAndSetDefaults(); err != nil {
		return nil, trace.BadParameter("invalid OIDC connector fields")
	}
	if err := connector.Validate(); err != nil {
		return nil, trace.BadParameter("invalid OIDC connector settings")
	}
	if _, err := auth.ZitadelConfigFromConnector(&connector); err != nil {
		return nil, trace.Wrap(err)
	}
	return &connector, nil
}

func validateZitadelTargetRoles(ctx context.Context, client authclient.ClientI, connector types.OIDCConnector) error {
	for _, mapping := range connector.GetClaimsToRoles() {
		for _, name := range mapping.Roles {
			if _, err := client.GetRole(ctx, name); err != nil {
				return trace.Wrap(err, "mapped Teleport role must exist and be readable")
			}
		}
	}
	return nil
}

func (h *Handler) createZitadelConnector(w http.ResponseWriter, r *http.Request, p httprouter.Params, session *SessionContext) (interface{}, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	client, err := session.GetClient()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return CreateResource(r, types.KindOIDC, unmarshalZitadelConnector, func(ctx context.Context, connector types.OIDCConnector) (types.OIDCConnector, error) {
		if connector.GetClientSecret() == zitadelHiddenSecret {
			return nil, trace.BadParameter("replace <existing-secret> with an empty value for None + PKCE or a client secret for Basic")
		}
		if err := validateZitadelTargetRoles(ctx, client, connector); err != nil {
			return nil, err
		}
		created, err := client.CreateOIDCConnector(ctx, connector)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return redactedZitadelConnector(created), nil
	})
}

func (h *Handler) updateZitadelConnector(w http.ResponseWriter, r *http.Request, p httprouter.Params, session *SessionContext) (interface{}, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	client, err := session.GetClient()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return UpdateResource(r, p, types.KindOIDC, unmarshalZitadelConnector, func(ctx context.Context, connector types.OIDCConnector) (types.OIDCConnector, error) {
		existing, err := client.GetOIDCConnector(ctx, connector.GetName(), true)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if existing.GetProvider() != "zitadel" {
			return nil, trace.NotFound("Zitadel connector not found")
		}
		if connector.GetRevision() == "" || connector.GetRevision() != existing.GetRevision() {
			return nil, trace.CompareFailed("connector changed; reopen the editor before saving")
		}
		if connector.GetClientSecret() == zitadelHiddenSecret {
			connector.SetClientSecret(existing.GetClientSecret())
		}
		if err := validateZitadelTargetRoles(ctx, client, connector); err != nil {
			return nil, err
		}
		updated, err := client.UpdateOIDCConnector(ctx, connector)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return redactedZitadelConnector(updated), nil
	})
}

func (h *Handler) deleteZitadelConnector(w http.ResponseWriter, r *http.Request, p httprouter.Params, session *SessionContext) (interface{}, error) {
	client, err := session.GetClient()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	connector, err := client.GetOIDCConnector(r.Context(), p.ByName("name"), false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if connector.GetProvider() != "zitadel" {
		return nil, trace.NotFound("Zitadel connector not found")
	}
	if err := client.DeleteOIDCConnector(r.Context(), connector.GetName()); err != nil {
		return nil, trace.Wrap(err)
	}
	name, kind, err := configuredAuthDefault(r.Context(), client)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if name == connector.GetName() && kind == constants.OIDC {
		items, err := supportedAuthConnectors(r.Context(), client)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		// Connector listing itself may still be cached just after deletion.
		items = slices.DeleteFunc(items, func(item ui.ResourceItem) bool {
			return item.Kind == constants.OIDC && item.Name == connector.GetName()
		})
		preference, err := authoritativeAuthPreference(r.Context(), client)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if len(items) == 0 {
			preference.SetType(constants.Local)
			preference.SetConnectorName("")
		} else {
			preference.SetType(items[len(items)-1].Kind)
			preference.SetConnectorName(items[len(items)-1].Name)
		}
		if _, err := client.UpsertAuthPreference(r.Context(), preference); err != nil {
			return nil, trace.Wrap(err)
		}
	}
	return OK(), nil
}
