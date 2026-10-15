// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/gravitational/teleport/api/types"
	zitadel "github.com/gravitational/teleport/lib/zitadeloidc"
	"github.com/gravitational/trace"
)

const (
	ZitadelProjectLabel      = "zitadel/project-id"
	ZitadelOrganizationLabel = "zitadel/organization-id"
	ZitadelTTLLabel          = "zitadel/transaction-ttl"
	ZitadelAMRLabel          = "zitadel/required-amr"
	ZitadelDisplayNameLabel  = "zitadel/display-name-claim"
)

type zitadelClientEntry struct {
	revision string
	client   *zitadel.Client
}

// ZitadelConfigFromConnector translates a native, trusted OIDC resource into
// the adapter's exact project and organization policy. Unknown or unsupported
// security settings fail instead of being silently ignored.
func ZitadelConfigFromConnector(connector types.OIDCConnector) (zitadel.Config, error) {
	c, ok := connector.(*types.OIDCConnectorV3)
	if !ok || c.GetProvider() != "zitadel" {
		return zitadel.Config{}, trace.BadParameter("provider must be zitadel")
	}
	if c.Spec.UsernameClaim != "" {
		return zitadel.Config{}, trace.BadParameter("username_claim changes account identity and is unsupported; use zitadel/display-name-claim for the display label")
	}
	if c.Spec.AllowUnverifiedEmail || c.Spec.ACR != "" || c.Spec.MFASettings != nil ||
		c.Spec.GoogleServiceAccount != "" || c.Spec.GoogleServiceAccountURI != "" || c.Spec.GoogleAdminEmail != "" ||
		c.Spec.EntraIdGroupsProvider != nil || len(c.Spec.Scope) != 0 ||
		(c.Spec.PKCEMode != "" && c.Spec.PKCEMode != "enabled") ||
		(c.Spec.RequestObjectMode != "" && c.Spec.RequestObjectMode != "disabled") {
		return zitadel.Config{}, trace.BadParameter("unsupported Zitadel connector security setting")
	}
	if len(c.GetRedirectURLs()) != 1 {
		return zitadel.Config{}, trace.BadParameter("Zitadel requires exactly one redirect URL")
	}
	labels := c.Metadata.Labels
	for key := range labels {
		if strings.HasPrefix(key, "zitadel/") && !slices.Contains([]string{ZitadelProjectLabel, ZitadelOrganizationLabel, ZitadelTTLLabel, ZitadelAMRLabel, ZitadelDisplayNameLabel}, key) {
			return zitadel.Config{}, trace.BadParameter("unknown Zitadel configuration label")
		}
	}
	cfg := zitadel.Config{
		IssuerURL: c.GetIssuerURL(), ClientID: c.GetClientID(), ClientSecret: c.GetClientSecret(), RedirectURL: c.GetRedirectURLs()[0],
		ProjectID: labels[ZitadelProjectLabel], OrganizationID: labels[ZitadelOrganizationLabel], RoleMappings: make(map[string][]string),
		DisplayNameClaim: labels[ZitadelDisplayNameLabel],
	}
	claim := "urn:zitadel:iam:org:project:" + cfg.ProjectID + ":roles"
	for _, mapping := range c.GetClaimsToRoles() {
		if mapping.Claim != claim {
			return zitadel.Config{}, trace.BadParameter("role mappings must use the configured project's role claim")
		}
		if _, duplicate := cfg.RoleMappings[mapping.Value]; duplicate {
			return zitadel.Config{}, trace.BadParameter("duplicate Zitadel role mapping")
		}
		cfg.RoleMappings[mapping.Value] = slices.Clone(mapping.Roles)
	}
	if value := labels[ZitadelTTLLabel]; value != "" {
		ttl, err := time.ParseDuration(value)
		if err != nil {
			return zitadel.Config{}, trace.BadParameter("invalid Zitadel transaction TTL")
		}
		cfg.TransactionTTL = ttl
	}
	if value := labels[ZitadelAMRLabel]; value != "" {
		if err := json.Unmarshal([]byte(value), &cfg.RequiredAMR); err != nil {
			return zitadel.Config{}, trace.BadParameter("required AMR must be a JSON array of strings")
		}
	}
	cfg.MaxAuthenticationAge, _ = c.GetMaxAge()
	data, err := json.Marshal(c)
	if err != nil {
		return zitadel.Config{}, trace.BadParameter("cannot bind Zitadel connector policy")
	}
	// Includes the connector name, secret and backend revision, so edits and
	// delete/recreate cycles cannot revive outstanding login transactions.
	digest := sha256.Sum256(data)
	cfg.PolicyRevision = hex.EncodeToString(digest[:])
	cfg, err = zitadel.ValidateConfig(cfg)
	if err != nil {
		return zitadel.Config{}, trace.BadParameter("invalid Zitadel settings: %s", err)
	}
	return cfg, nil
}

func (s *zitadelService) clientForConnector(ctx context.Context, id string) (types.OIDCConnector, zitadel.Config, *zitadel.Client, error) {
	// Read the backend rather than a resource cache on every login and callback.
	connector, err := s.auth.Services.GetOIDCConnector(ctx, id, true)
	if err != nil {
		return nil, zitadel.Config{}, nil, trace.Wrap(err)
	}
	cfg, err := ZitadelConfigFromConnector(connector)
	if err != nil {
		return nil, zitadel.Config{}, nil, trace.Wrap(err)
	}
	s.mu.Lock()
	entry, found := s.clients[id]
	s.mu.Unlock()
	if found && entry.revision == cfg.PolicyRevision {
		return connector, cfg, entry.client, nil
	}
	// Network discovery must not hold the shared cache mutex.
	client, err := zitadel.New(s.auth.CloseContext(), cfg, s.store, nil)
	if err != nil {
		return nil, zitadel.Config{}, nil, trace.Wrap(err, "initialize Zitadel provider")
	}
	s.mu.Lock()
	if len(s.clients) >= 64 {
		for key := range s.clients {
			delete(s.clients, key)
			break
		}
	}
	s.clients[id] = zitadelClientEntry{revision: cfg.PolicyRevision, client: client}
	s.mu.Unlock()
	return connector, cfg, client, nil
}

func (s *zitadelService) migrateLegacyConnector(ctx context.Context, id string, cfg zitadel.Config) error {
	connector, err := s.auth.Services.GetOIDCConnector(ctx, id, true)
	if trace.IsNotFound(err) {
		// A deleted connector stays deleted. The Web UI can create another one.
		return nil
	}
	if err != nil {
		return trace.Wrap(err)
	}
	if connector.GetProvider() == "zitadel" {
		// Native settings are authoritative, including after a restart.
		return nil
	}
	if err := validateLegacyZitadelConnector(connector, cfg); err != nil {
		return trace.Wrap(err)
	}
	c, ok := connector.(*types.OIDCConnectorV3)
	if !ok {
		return trace.BadParameter("unsupported legacy Zitadel connector version")
	}
	c.Spec.Provider = "zitadel"
	c.Metadata.Labels = maps.Clone(c.Metadata.Labels)
	if c.Metadata.Labels == nil {
		c.Metadata.Labels = make(map[string]string)
	}
	c.Metadata.Labels[ZitadelProjectLabel] = cfg.ProjectID
	c.Metadata.Labels[ZitadelOrganizationLabel] = cfg.OrganizationID
	c.Metadata.Labels[ZitadelTTLLabel] = cfg.TransactionTTL.String()
	c.Metadata.Labels[ZitadelDisplayNameLabel] = cfg.DisplayNameClaim
	if len(cfg.RequiredAMR) > 0 {
		value, _ := json.Marshal(cfg.RequiredAMR)
		c.Metadata.Labels[ZitadelAMRLabel] = string(value)
	}
	if cfg.MaxAuthenticationAge > 0 {
		c.Spec.MaxAge = &types.MaxAge{Value: types.NewDuration(cfg.MaxAuthenticationAge)}
	}
	if _, err := ZitadelConfigFromConnector(c); err != nil {
		return trace.Wrap(err)
	}
	_, err = s.auth.UpdateOIDCConnector(ctx, c)
	return trace.Wrap(err)
}
