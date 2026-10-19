package zitadel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Client serves a single immutable Zitadel application and authorization policy.
// Construct it with the host service's lifecycle context, not a login's context.
type Client struct {
	cfg      Config
	store    Store
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	http     *http.Client
	binding  string
	now      func() time.Time
}

// Authorization contains only public redirect data. The PKCE verifier and
// original host request remain exclusively in Store. The authorization URL
// includes the public nonce and PKCE challenge sent to the provider.
type Authorization struct {
	URL       string
	State     string
	ExpiresAt time.Time
}

// Authentication is produced once, after protocol and authorization checks.
// The host must independently apply its roles, locks, certificate lifetime,
// user ownership, session rules, and audit requirements before issuing access.
type Authentication struct {
	Identity        Identity
	Request         []byte
	AuthenticatedAt time.Time
	// TokenExpiry is supplied for host policy, not a Teleport certificate TTL.
	TokenExpiry time.Time
}

func New(ctx context.Context, cfg Config, store Store, client *http.Client) (*Client, error) {
	cfg, err := cfg.validated()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("transaction store is required")
	}
	origin, _ := url.Parse(cfg.IssuerURL)
	if client == nil {
		client = http.DefaultClient
	}
	guarded := *client
	if guarded.Timeout <= 0 || guarded.Timeout > 15*time.Second {
		guarded.Timeout = 15 * time.Second
	}
	transport := guarded.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	guarded.Transport = &providerTransport{origin: origin, inner: transport}
	// Redirects must never forward token/client credentials to another endpoint.
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	providerCtx := oidc.ClientContext(ctx, &guarded)
	provider, err := oidc.NewProvider(providerCtx, cfg.IssuerURL)
	if err != nil {
		return nil, providerError(ctx)
	}
	var discovery struct {
		JWKS string `json:"jwks_uri"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, ErrProvider
	}
	for _, endpoint := range []string{provider.Endpoint().AuthURL, provider.Endpoint().TokenURL, provider.UserInfoEndpoint(), discovery.JWKS} {
		u, err := url.Parse(endpoint)
		if err != nil || httpsURL(endpoint) != nil || !sameOrigin(u, origin) {
			return nil, errors.New("Zitadel discovery endpoints must use HTTPS on the configured issuer origin")
		}
	}
	// Pin the policy and credentials into outstanding transactions. A policy,
	// secret, or connector change invalidates requests created by the old client.
	policy, err := json.Marshal(struct {
		Config   Config
		Secret   string
		TTL      time.Duration
		MaxAge   time.Duration
		Revision string
	}{cfg, cfg.ClientSecret, cfg.TransactionTTL, cfg.MaxAuthenticationAge, cfg.PolicyRevision})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(policy)
	c := &Client{
		cfg: cfg, store: store, provider: provider, http: &guarded,
		binding: hex.EncodeToString(digest[:]), now: time.Now,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			RedirectURL: cfg.RedirectURL,
			Endpoint:    oauth2.Endpoint{AuthURL: provider.Endpoint().AuthURL, TokenURL: provider.Endpoint().TokenURL, AuthStyle: oauth2.AuthStyleInHeader},
			Scopes: []string{oidc.ScopeOpenID, "profile", "email",
				"urn:zitadel:iam:org:project:id:" + cfg.ProjectID + ":aud",
				"urn:zitadel:iam:org:projects:roles"},
		},
	}
	c.verifier = provider.VerifierContext(providerCtx, &oidc.Config{
		ClientID:             cfg.ClientID,
		SupportedSigningAlgs: []string{oidc.RS256},
		Now:                  func() time.Time { return c.now() },
	})
	return c, nil
}

// Begin binds the authorization flow to trusted host data. The host must also
// correlate browser/CLI requests using its existing login transaction controls.
func (c *Client) Begin(ctx context.Context, request []byte) (Authorization, error) {
	if err := ctx.Err(); err != nil {
		return Authorization{}, err
	}
	if len(request) == 0 || len(request) > 64<<10 {
		return Authorization{}, errors.New("host request must contain 1–65536 bytes")
	}
	state, err := randomValue()
	if err != nil {
		return Authorization{}, err
	}
	nonce, err := randomValue()
	if err != nil {
		return Authorization{}, err
	}
	verifier, err := randomValue()
	if err != nil {
		return Authorization{}, err
	}
	now := c.now()
	tx := Transaction{
		State: state, Nonce: nonce, Verifier: verifier, Binding: c.binding,
		CreatedAt: now, ExpiresAt: now.Add(c.cfg.TransactionTTL), Request: slices.Clone(request),
	}
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if c.cfg.MaxAuthenticationAge > 0 {
		options = append(options, oauth2.SetAuthURLParam("max_age", strconv.FormatInt(int64(c.cfg.MaxAuthenticationAge/time.Second), 10)))
	}
	if err := c.store.Put(ctx, tx); err != nil {
		return Authorization{}, err
	}
	return Authorization{URL: c.oauth.AuthCodeURL(state, options...), State: state, ExpiresAt: tx.ExpiresAt}, nil
}

// Complete consumes the transaction before exchanging the code. Failed or
// denied callbacks must start a new login; they cannot retry the same state.
// Provider descriptions, codes, secrets, and tokens are never included in errors.
func (c *Client) Complete(ctx context.Context, query url.Values) (result *Authentication, err error) {
	stage, reason, status := "state", "invalid_expired_or_consumed_state", 0
	var roleGrantOrganizations []string
	defer func() {
		if err != nil {
			var diagnosed *AuthenticationError
			if !errors.As(err, &diagnosed) {
				err = &AuthenticationError{Stage: stage, Code: reason, HTTPStatus: status, RoleGrantOrganizations: roleGrantOrganizations, cause: err}
			}
		}
	}()
	states := query["state"]
	if len(states) != 1 || len(states[0]) != 43 {
		return nil, ErrTransaction
	}
	tx, err := c.store.Consume(ctx, states[0])
	if err != nil {
		return nil, err
	}
	now := c.now()
	if tx.State != states[0] || tx.Nonce == "" || tx.Verifier == "" ||
		subtle.ConstantTimeCompare([]byte(tx.Binding), []byte(c.binding)) != 1 ||
		!now.Before(tx.ExpiresAt) || tx.CreatedAt.After(now) || len(tx.Request) == 0 {
		return nil, ErrTransaction
	}
	stage, reason = "callback", "invalid_callback_parameters"
	for _, key := range []string{"code", "error", "error_description", "iss"} {
		if values, exists := query[key]; exists && len(values) != 1 {
			return nil, ErrDenied
		}
	}
	if _, failed := query["error"]; failed {
		reason = oauthFailureCode(query.Get("error"))
		return nil, ErrDenied
	}
	if issuer, supplied := query["iss"]; supplied && issuer[0] != c.cfg.IssuerURL {
		return nil, ErrDenied
	}
	code := query.Get("code")
	if code == "" || len(code) > 4096 {
		return nil, ErrDenied
	}
	providerCtx := oidc.ClientContext(ctx, c.http)
	stage, reason = "token_exchange", "provider_request_failed"
	token, err := c.oauth.Exchange(providerCtx, code, oauth2.VerifierOption(tx.Verifier))
	if err != nil {
		reason, status = tokenExchangeFailure(err)
		return nil, providerError(ctx)
	}
	rawToken, ok := token.Extra("id_token").(string)
	stage, reason = "token_validation", "missing_id_or_access_token"
	if !ok || rawToken == "" || token.AccessToken == "" {
		return nil, ErrDenied
	}
	idToken, err := c.verifier.Verify(providerCtx, rawToken)
	if err != nil {
		reason = tokenValidationFailure(rawToken, err)
		return nil, ErrDenied
	}
	reason = "invalid_subject_or_token_times"
	if idToken.Subject == "" || idToken.IssuedAt.IsZero() || idToken.Expiry.IsZero() ||
		idToken.IssuedAt.After(c.now().Add(30*time.Second)) || !idToken.IssuedAt.Before(idToken.Expiry) ||
		!c.now().Before(idToken.Expiry) {
		return nil, ErrDenied
	}
	reason = "nonce_mismatch"
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(tx.Nonce)) != 1 {
		return nil, ErrDenied
	}
	if idToken.AccessTokenHash != "" {
		reason = "access_token_hash_mismatch"
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			return nil, ErrDenied
		}
	}
	var claims map[string]json.RawMessage
	stage, reason = "claims", "invalid_claim_types"
	if err := idToken.Claims(&claims); err != nil {
		return nil, ErrDenied
	}
	var security struct {
		AuthorizedParty string   `json:"azp"`
		AuthTime        int64    `json:"auth_time"`
		AMR             []string `json:"amr"`
	}
	if err := idToken.Claims(&security); err != nil {
		return nil, ErrDenied
	}
	reason = "authorized_party_mismatch_or_missing"
	if (security.AuthorizedParty != "" && security.AuthorizedParty != c.cfg.ClientID) ||
		(len(idToken.Audience) > 1 && security.AuthorizedParty == "") {
		return nil, ErrDenied
	}
	if _, exists := claims["azp"]; exists && security.AuthorizedParty == "" {
		return nil, ErrDenied
	}
	if c.cfg.MaxAuthenticationAge > 0 {
		reason = "authentication_too_old_or_missing_auth_time"
		authTime := time.Unix(security.AuthTime, 0)
		if security.AuthTime <= 0 || authTime.After(c.now().Add(30*time.Second)) || c.now().Sub(authTime) > c.cfg.MaxAuthenticationAge {
			return nil, ErrDenied
		}
	}
	for _, required := range c.cfg.RequiredAMR {
		reason = "required_authentication_method_missing"
		if !slices.Contains(security.AMR, required) {
			return nil, ErrDenied
		}
	}
	roleClaim := projectRolesClaim(c.cfg.ProjectID)
	// Zitadel can deliver email and project grants through UserInfo instead of
	// the ID token. Only fill missing claims; never overwrite signed claims.
	_, hasRoles := claims[roleClaim]
	_, hasUsername := claims[c.cfg.UsernameClaim]
	userInfoRequired := !hasRoles || claims["email"] == nil || claims["email_verified"] == nil ||
		(c.cfg.UsernameClaim != "" && !hasUsername)
	_, hasDisplayName := claims[c.cfg.DisplayNameClaim]
	traitNames := traitClaimNames(c.cfg)
	if userInfoRequired || !hasDisplayName || missingTraitClaims(claims, traitNames) {
		stage, reason = "userinfo", "provider_request_failed"
		info, err := c.provider.UserInfo(providerCtx, oauth2.StaticTokenSource(token))
		if err != nil {
			// Optional display/profile lookups may fail without blocking an
			// otherwise complete login. Missing traits remain absent.
			if userInfoRequired {
				return nil, providerError(ctx)
			}
		} else {
			if info.Subject != idToken.Subject {
				reason = "userinfo_subject_mismatch"
				return nil, ErrDenied
			}
			var additional map[string]json.RawMessage
			reason = "invalid_userinfo_claims"
			if err := info.Claims(&additional); err != nil {
				return nil, ErrDenied
			}
			// An email and its verification flag must describe the same address.
			// Never attach UserInfo's verification flag to a different signed email.
			if claims["email_verified"] == nil && claims["email"] != nil {
				reason = "email_verification_address_mismatch"
				var signedEmail, infoEmail string
				if json.Unmarshal(claims["email"], &signedEmail) != nil ||
					json.Unmarshal(additional["email"], &infoEmail) != nil || signedEmail != infoEmail {
					return nil, ErrDenied
				}
			}
			if claims["email"] == nil {
				reason = "userinfo_email_unverified_or_missing"
				var infoVerified bool
				if json.Unmarshal(additional["email_verified"], &infoVerified) != nil || !infoVerified {
					return nil, ErrDenied
				}
			}
			keys := []string{roleClaim, "email", "email_verified", c.cfg.DisplayNameClaim}
			if c.cfg.UsernameClaim != "" {
				keys = append(keys, c.cfg.UsernameClaim)
			}
			for _, name := range traitNames {
				if !idTokenTrait(name) {
					keys = append(keys, name)
				}
			}
			for _, key := range keys {
				if idTokenTrait(key) {
					continue
				}
				if _, exists := claims[key]; !exists {
					claims[key] = additional[key]
				}
			}
		}
	}
	var email string
	var emailVerified bool
	stage, reason = "email", "email_unverified_missing_or_invalid"
	if err := json.Unmarshal(claims["email"], &email); err != nil {
		return nil, ErrDenied
	}
	if err := json.Unmarshal(claims["email_verified"], &emailVerified); err != nil {
		return nil, ErrDenied
	}
	if email == "" || !emailVerified {
		return nil, ErrDenied
	}
	var username string
	if c.cfg.UsernameClaim != "" {
		stage, reason = "username", "username_claim_missing_or_invalid"
		username, err = usernameClaim(claims[c.cfg.UsernameClaim])
		if err != nil {
			return nil, err
		}
	}
	stage, reason = "role_mapping", "no_matching_role_for_configured_organization"
	if len(claims[roleClaim]) == 0 {
		reason = "project_role_claim_missing"
		if len(claims["urn:zitadel:iam:org:project:roles"]) > 0 {
			reason = "only_generic_project_role_claim_present"
		}
	}
	roles, err := mapRoles(claims[roleClaim], c.cfg)
	if err != nil {
		// Only inspected after identity validation, for the configured project's
		// allowlisted role keys. Other organizations still cannot grant access.
		roleGrantOrganizations = mappedRoleGrantOrganizations(claims[roleClaim], c.cfg)
		if len(roleGrantOrganizations) > 0 {
			reason = "mapped_role_granted_in_other_organization"
		}
		return nil, err
	}
	// Network operations may outlive the original transaction or token.
	stage, reason = "completion", "transaction_or_token_expired"
	if !c.now().Before(tx.ExpiresAt) || !c.now().Before(idToken.Expiry) {
		return nil, ErrDenied
	}
	if c.cfg.MaxAuthenticationAge > 0 && c.now().Sub(time.Unix(security.AuthTime, 0)) > c.cfg.MaxAuthenticationAge {
		return nil, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Authentication{
		Identity: Identity{Issuer: idToken.Issuer, Subject: idToken.Subject, Email: email, Username: username,
			DisplayName: displayName(claims[c.cfg.DisplayNameClaim], email), Roles: roles,
			Traits: identityTraits(claims, traitNames, idToken.Issuer, idToken.Subject, email)},
		Request: slices.Clone(tx.Request), AuthenticatedAt: c.now(), TokenExpiry: idToken.Expiry,
	}, nil
}

func randomValue() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate OIDC transaction: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func providerError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrProvider
}

type providerTransport struct {
	origin *url.URL
	inner  http.RoundTripper
}

func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host }

func (t *providerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if httpsURL(req.URL.String()) != nil || !sameOrigin(req.URL, t.origin) {
		return nil, ErrProvider
	}
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, 1<<20)
	return resp, nil
}
