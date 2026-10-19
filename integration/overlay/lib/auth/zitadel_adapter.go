// SPDX-License-Identifier: AGPL-3.0-or-later
package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/api/constants"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	apievents "github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/api/utils/keys/hardwarekey"
	"github.com/gravitational/teleport/lib/auth/authclient"
	"github.com/gravitational/teleport/lib/authz"
	"github.com/gravitational/teleport/lib/backend"
	"github.com/gravitational/teleport/lib/client/sso"
	"github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/httplib"
	"github.com/gravitational/teleport/lib/loginrule"
	"github.com/gravitational/teleport/lib/services"
	zitadel "github.com/gravitational/teleport/lib/zitadeloidc"
	"github.com/gravitational/trace"
	"github.com/julienschmidt/httprouter"
)

type zitadelService struct {
	auth    *Server
	store   zitadel.Store
	mu      sync.Mutex
	clients map[string]zitadelClientEntry
}

var _ OIDCService = (*zitadelService)(nil)

// ConfigureZitadelAdapter is called before the Auth TLS server starts serving.
// An optional file supplies legacy bootstrap settings. After migration,
// connectors and credentials are managed as native Teleport resources.
func (a *Server) ConfigureZitadelAdapter(ctx context.Context) error {
	store, err := zitadel.NewBackendStore(zitadelBackend{backend: a.bk})
	if err != nil {
		return trace.Wrap(err)
	}
	service := &zitadelService{auth: a, store: store, clients: make(map[string]zitadelClientEntry)}
	if os.Getenv("TELEPORT_ZITADEL_CONFIG") == "" {
		a.SetOIDCService(service)
		return nil
	}
	id := os.Getenv("TELEPORT_ZITADEL_CONNECTOR_ID")
	if id == "" {
		id = "zitadel"
	}
	existing, err := a.Services.GetOIDCConnector(ctx, id, false)
	if trace.IsNotFound(err) || (err == nil && existing.GetProvider() == "zitadel") {
		a.SetOIDCService(service)
		return nil
	}
	if err != nil {
		return trace.Wrap(err)
	}
	file, err := os.Open(os.Getenv("TELEPORT_ZITADEL_CONFIG"))
	if err != nil {
		return trace.Wrap(err, "open Zitadel adapter configuration")
	}
	defer file.Close()
	secret, err := os.ReadFile(os.Getenv("TELEPORT_ZITADEL_SECRET_FILE"))
	if err != nil {
		return trace.Wrap(err, "read Zitadel client secret file")
	}
	cfg, err := zitadel.ReadConfig(file, strings.TrimSpace(string(secret)))
	if err != nil {
		return trace.Wrap(err)
	}
	if err := service.migrateLegacyConnector(ctx, id, cfg); err != nil {
		return trace.Wrap(err)
	}
	a.SetOIDCService(service)
	return nil
}

func validateLegacyZitadelConnector(c types.OIDCConnector, cfg zitadel.Config) error {
	if c.GetIssuerURL() != cfg.IssuerURL || c.GetClientID() != cfg.ClientID ||
		subtle.ConstantTimeCompare([]byte(c.GetClientSecret()), []byte(cfg.ClientSecret)) != 1 ||
		!slices.Equal(c.GetRedirectURLs(), []string{cfg.RedirectURL}) || c.IsMFAEnabled() {
		return trace.AccessDenied("Zitadel connector differs from legacy adapter configuration")
	}
	expected := make(map[string][]string, len(cfg.RoleMappings))
	for role, targets := range cfg.RoleMappings {
		expected[role] = slices.Clone(targets)
		slices.Sort(expected[role])
	}
	actual := make(map[string][]string)
	claim := "urn:zitadel:iam:org:project:" + cfg.ProjectID + ":roles"
	for _, mapping := range c.GetClaimsToRoles() {
		if mapping.Claim != claim {
			return trace.AccessDenied("unsupported Zitadel connector mapping")
		}
		if _, duplicate := actual[mapping.Value]; duplicate {
			return trace.AccessDenied("duplicate Zitadel connector mapping")
		}
		actual[mapping.Value] = slices.Clone(mapping.Roles)
		slices.Sort(actual[mapping.Value])
	}
	if !maps.EqualFunc(expected, actual, slices.Equal[[]string]) {
		return trace.AccessDenied("Zitadel role mappings differ from legacy adapter configuration")
	}
	return nil
}

func (s *zitadelService) CreateOIDCAuthRequest(ctx context.Context, req types.OIDCAuthRequest) (*types.OIDCAuthRequest, error) {
	if req.SSOTestFlow || req.ConnectorSpec != nil || req.Type != "" {
		return nil, trace.NotImplemented("Zitadel connector test and MFA flows are not implemented")
	}
	c, _, client, err := s.clientForConnector(ctx, req.ConnectorID)
	if err != nil {
		return nil, err
	}
	if req.CreateWebSession {
		if req.CSRFToken == "" {
			return nil, trace.AccessDenied("browser CSRF token is required")
		}
	} else {
		if len(req.SshPublicKey)+len(req.TlsPublicKey) == 0 {
			return nil, trace.BadParameter("client public key is required")
		}
		if err := sso.ValidateClientRedirect(req.ClientRedirectURL, sso.CeremonyTypeLogin, c.GetClientRedirectSettings()); err != nil {
			return nil, trace.Wrap(err, InvalidClientRedirectErrorMessage)
		}
	}
	if req.CertTTL < 0 {
		return nil, trace.BadParameter("negative session TTL")
	}
	req.StateToken, req.RedirectURL, req.PkceVerifier = "", "", ""
	data, err := req.Marshal()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	redirect, err := client.Begin(ctx, data)
	if err != nil {
		s.auth.logger.WarnContext(ctx, "Cannot persist Zitadel login transaction", "error", err)
		return nil, trace.AccessDenied("cannot start Zitadel login")
	}
	req.StateToken, req.RedirectURL = redirect.State, redirect.URL
	// Preserve the standard request lookup for tsh's failed-login redirect.
	// The separately stored adapter transaction is still the sole authority
	// for successful callbacks and is consumed atomically.
	if err := s.auth.Services.CreateOIDCAuthRequest(ctx, req, time.Until(redirect.ExpiresAt)); err != nil {
		return nil, trace.Wrap(err)
	}
	return &req, nil
}

func (*zitadelService) CreateOIDCAuthRequestForMFA(context.Context, types.OIDCAuthRequest) (*types.OIDCAuthRequest, error) {
	return nil, trace.NotImplemented("Zitadel SSO MFA challenges are not implemented")
}

func (s *zitadelService) ValidateOIDCAuthCallback(ctx context.Context, q url.Values) (response *authclient.OIDCAuthResponse, err error) {
	event := &apievents.UserLogin{Metadata: apievents.Metadata{Type: events.UserLoginEvent}, Method: events.LoginMethodOIDC, ConnectionMetadata: authz.ConnectionMetadata(ctx)}
	defer func() {
		event.Code = events.UserSSOLoginFailureCode
		if err == nil {
			event.Code, event.User, event.Success = events.UserSSOLoginCode, response.Username, true
		} else {
			event.Status.UserMessage = "Zitadel authentication failed"
		}
		if auditErr := s.auth.EmitAuditEvent(ctx, event); auditErr != nil {
			s.auth.logger.WarnContext(ctx, "Failed to emit Zitadel login audit event", "error", auditErr)
		}
	}()
	if states := q["state"]; len(states) != 1 || len(states[0]) != 43 {
		return nil, trace.AccessDenied("invalid Zitadel login state")
	}
	// The connector comes from the trusted stored request, never the callback.
	stored, err := s.auth.Services.GetOIDCAuthRequest(ctx, q.Get("state"))
	if err != nil {
		return nil, trace.AccessDenied("invalid or expired Zitadel login state")
	}
	_, cfg, client, err := s.clientForConnector(ctx, stored.ConnectorID)
	if err != nil {
		return nil, trace.AccessDenied("Zitadel connector unavailable")
	}
	result, err := client.Complete(ctx, q)
	if err != nil {
		var diagnostic *zitadel.AuthenticationError
		if errors.As(err, &diagnostic) {
			s.auth.logger.WarnContext(ctx, "Zitadel authentication rejected",
				"stage", diagnostic.Stage, "reason", diagnostic.Code, "provider_status", diagnostic.HTTPStatus,
				"role_grant_organizations", diagnostic.RoleGrantOrganizations)
		}
		return nil, trace.AccessDenied("Zitadel authentication failed")
	}
	var req types.OIDCAuthRequest
	if err := req.Unmarshal(result.Request); err != nil {
		return nil, trace.AccessDenied("invalid stored Zitadel request")
	}
	current, err := s.auth.Services.GetOIDCConnector(ctx, req.ConnectorID, true)
	if err != nil {
		return nil, err
	}
	latest, err := ZitadelConfigFromConnector(current)
	if err != nil || req.ConnectorID != stored.ConnectorID || subtle.ConstantTimeCompare([]byte(latest.PolicyRevision), []byte(cfg.PolicyRevision)) != 1 {
		return nil, trace.AccessDenied("Zitadel connector changed during authentication")
	}
	return s.issue(ctx, &req, result.Identity)
}

func zitadelUsername(connector string, identity zitadel.Identity) string {
	if identity.Username != "" {
		return identity.Username
	}
	digest := sha256.Sum256([]byte(connector + "\x00" + identity.Issuer + "\x00" + identity.Subject))
	return "zitadel-" + hex.EncodeToString(digest[:16])
}

func zitadelTraits(identity zitadel.Identity) map[string][]string {
	traits := make(map[string][]string, len(identity.Traits)+5)
	for name, values := range identity.Traits {
		traits[name] = slices.Clone(values)
	}
	traits["email"] = []string{identity.Email}
	bindZitadelIdentityTraits(traits, identity)
	return traits
}

func bindZitadelIdentityTraits(traits map[string][]string, identity zitadel.Identity) {
	traits["iss"] = []string{identity.Issuer}
	traits["sub"] = []string{identity.Subject}
	traits["zitadel_issuer"] = []string{identity.Issuer}
	traits["zitadel_subject"] = []string{identity.Subject}
}

func (s *zitadelService) issue(ctx context.Context, req *types.OIDCAuthRequest, identity zitadel.Identity) (*authclient.OIDCAuthResponse, error) {
	a := s.auth
	username := zitadelUsername(req.ConnectorID, identity)
	if !backend.IsKeySafe(backend.NewKey(username)) {
		return nil, trace.AccessDenied("invalid Zitadel account name")
	}
	traits := zitadelTraits(identity)
	evaluation, err := a.GetLoginRuleEvaluator().Evaluate(ctx, &loginrule.EvaluationInput{Traits: traits})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	traits = maps.Clone(evaluation.Traits)
	if traits == nil {
		traits = make(map[string][]string)
	}
	// Account ownership must survive profile changes and login-rule transforms.
	bindZitadelIdentityTraits(traits, identity)
	roleSet, err := services.FetchRolesWithContext(identity.Roles, a, services.RoleTemplateContext{Username: username, Traits: traits})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	ttl := roleSet.AdjustSessionTTL(apidefaults.MaxCertDuration)
	if req.CertTTL > 0 && req.CertTTL < ttl {
		ttl = req.CertTTL
	}
	if ttl <= 0 {
		return nil, trace.AccessDenied("Zitadel session lifetime is zero")
	}
	expires := a.GetClock().Now().UTC().Add(ttl)
	label := identity.DisplayName
	if label == "" {
		label = identity.Email
	}
	external := types.ExternalIdentity{ConnectorID: req.ConnectorID, Username: label, UserID: identity.Subject}
	user := &types.UserV2{
		Kind: types.KindUser, Version: types.V2, Metadata: types.Metadata{Name: username, Namespace: apidefaults.Namespace, Expires: &expires},
		Spec: types.UserSpecV2{Roles: identity.Roles, Traits: traits, OIDCIdentities: []types.ExternalIdentity{external},
			CreatedBy: types.CreatedBy{User: types.UserRef{Name: teleport.UserSystem}, Time: a.GetClock().Now().UTC(),
				Connector: &types.ConnectorRef{Type: constants.OIDC, ID: req.ConnectorID, Identity: identity.Subject}}},
	}
	if err := upsertZitadelUser(ctx, a.Services, user, req.ConnectorID, identity); err != nil {
		return nil, trace.Wrap(err)
	}
	if err := a.CallLoginHooks(ctx, user); err != nil {
		return nil, trace.Wrap(err)
	}
	state, err := a.GetUserOrLoginState(ctx, username)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if len(state.GetRoles()) == 0 {
		return nil, trace.AccessDenied("no effective Zitadel roles")
	}
	effective, err := services.FetchRolesWithContext(state.GetRoles(), a, services.RoleTemplateContext{Username: username, Traits: state.GetTraits()})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if effectiveTTL := effective.AdjustSessionTTL(ttl); effectiveTTL < ttl {
		ttl = effectiveTTL
	}
	response := &authclient.OIDCAuthResponse{Username: username, Identity: external, Req: authclient.OIDCAuthRequest{
		ConnectorID: req.ConnectorID, CSRFToken: req.CSRFToken, SSHPubKey: req.SshPublicKey, TLSPubKey: req.TlsPublicKey,
		CreateWebSession: req.CreateWebSession, ClientRedirectURL: req.ClientRedirectURL}}
	if req.CreateWebSession {
		response.Session, err = a.CreateWebSessionFromReq(ctx, NewWebSessionRequest{User: username, Roles: state.GetRoles(), Traits: state.GetTraits(),
			SessionTTL: ttl, LoginTime: a.GetClock().Now().UTC(), LoginIP: req.ClientLoginIP, LoginUserAgent: req.ClientUserAgent,
			AttestWebSession: true, CreateDeviceWebToken: true, Scope: req.Scope})
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	if len(req.SshPublicKey)+len(req.TlsPublicKey) > 0 {
		response.Cert, response.TLSCert, err = a.CreateSessionCerts(ctx, &SessionCertsRequest{UserState: state, SessionTTL: ttl,
			SSHPubKey: req.SshPublicKey, TLSPubKey: req.TlsPublicKey, SSHAttestationStatement: hardwarekey.AttestationStatementFromProto(req.SshAttestationStatement),
			TLSAttestationStatement: hardwarekey.AttestationStatementFromProto(req.TlsAttestationStatement), Compatibility: req.Compatibility,
			RouteToCluster: req.RouteToCluster, KubernetesCluster: req.KubernetesCluster, LoginIP: req.ClientLoginIP, Scope: req.Scope})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		cluster, err := a.GetClusterName(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		ca, err := a.GetCertAuthority(ctx, types.CertAuthID{Type: types.HostCA, DomainName: cluster.GetClusterName()}, false)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		response.HostSigners = []types.CertAuthority{ca}
	}
	response.ClientOptions, err = a.ClientOptionsForLogin(state)
	return response, trace.Wrap(err)
}

func zitadelOwnsUser(user types.User, connector string, identity zitadel.Identity) bool {
	ref := user.GetCreatedBy().Connector
	ids := user.GetOIDCIdentities()
	traits := user.GetTraits()
	return identity.Issuer != "" && identity.Subject != "" && ref != nil &&
		ref.Type == constants.OIDC && ref.ID == connector && ref.Identity == identity.Subject &&
		len(ids) == 1 && ids[0].ConnectorID == connector && ids[0].UserID == identity.Subject &&
		slices.Equal(traits["zitadel_issuer"], []string{identity.Issuer}) &&
		slices.Equal(traits["zitadel_subject"], []string{identity.Subject})
}

type zitadelBackend struct{ backend backend.Backend }

func (b zitadelBackend) Create(ctx context.Context, key string, value []byte, expiry time.Time) error {
	_, err := b.backend.Create(ctx, backend.Item{Key: backend.NewKey(strings.Split(key, "/")...), Value: value, Expires: expiry})
	if trace.IsAlreadyExists(err) {
		return zitadel.ErrBackendConflict
	}
	return err
}
func (b zitadelBackend) Get(ctx context.Context, key string) ([]byte, error) {
	item, err := b.backend.Get(ctx, backend.NewKey(strings.Split(key, "/")...))
	if trace.IsNotFound(err) {
		return nil, zitadel.ErrBackendNotFound
	}
	if err != nil {
		return nil, err
	}
	return item.Value, nil
}
func (b zitadelBackend) CompareAndSwap(ctx context.Context, key string, old, replacement []byte, expiry time.Time) error {
	item, err := b.backend.Get(ctx, backend.NewKey(strings.Split(key, "/")...))
	if trace.IsNotFound(err) {
		return zitadel.ErrBackendNotFound
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(item.Value, old) {
		return zitadel.ErrBackendConflict
	}
	updated := *item
	updated.Value, updated.Expires = replacement, expiry
	_, err = b.backend.CompareAndSwap(ctx, *item, updated)
	if trace.IsCompareFailed(err) {
		return zitadel.ErrBackendConflict
	}
	if trace.IsNotFound(err) {
		return zitadel.ErrBackendNotFound
	}
	return err
}

// RegisterZitadelHandlers uses the existing Auth HTTP authorization middleware.
func (s *APIServer) RegisterZitadelHandlers() {
	s.POST("/:version/oidc/requests/validate", s.WithAuth(s.validateZitadelCallback))
	s.GET("/:version/zitadel/authpreference", s.WithAuth(s.zitadelAuthPreference))
}

func (s *APIServer) zitadelAuthPreference(a *ServerWithRoles, w http.ResponseWriter, r *http.Request, p httprouter.Params, version string) (interface{}, error) {
	if err := a.authorizeAction(types.KindClusterAuthPreference, types.VerbRead); err != nil {
		return nil, trace.Wrap(err)
	}
	// Read the backend directly so a recent Set Default cannot be hidden by
	// the Auth service's read-only configuration cache.
	return a.authServer.Services.GetAuthPreference(r.Context())
}
func (s *APIServer) validateZitadelCallback(a *ServerWithRoles, w http.ResponseWriter, r *http.Request, p httprouter.Params, version string) (interface{}, error) {
	var req authclient.ValidateOIDCAuthCallbackReq
	if err := httplib.ReadJSON(r, &req); err != nil {
		return nil, trace.Wrap(err)
	}
	response, err := a.ValidateOIDCAuthCallback(r.Context(), req.Query)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	raw := authclient.OIDCAuthRawResponse{Username: response.Username, Identity: response.Identity, Cert: response.Cert, TLSCert: response.TLSCert, Req: response.Req, ClientOptions: response.ClientOptions}
	if response.Session != nil {
		raw.Session, err = services.MarshalWebSession(response.Session, services.WithVersion(version), services.PreserveRevision())
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}
	for _, ca := range response.HostSigners {
		data, err := services.MarshalCertAuthority(ca, services.WithVersion(version), services.PreserveRevision())
		if err != nil {
			return nil, trace.Wrap(err)
		}
		raw.HostSigners = append(raw.HostSigners, json.RawMessage(data))
	}
	return &raw, nil
}
