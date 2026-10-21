// Package zitadel implements OIDC authentication and explicit Zitadel project
// role mapping. Teleport session and certificate issuance belong in the host.
package zitadel

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

var (
	ErrDenied      = errors.New("OIDC authentication denied")
	ErrTransaction = errors.New("OIDC transaction missing, expired, or already consumed")
	ErrProvider    = errors.New("OIDC provider request failed")
	ErrCapacity    = errors.New("OIDC transaction capacity reached")
)

// Config is trusted, server-side configuration for one Zitadel application.
// An empty ClientSecret selects None + PKCE. A supplied secret selects Basic
// authentication and must come from a trusted secret source.
type Config struct {
	IssuerURL      string `json:"issuer_url"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"-"`
	RedirectURL    string `json:"redirect_url"`
	ProjectID      string `json:"project_id"`
	OrganizationID string `json:"organization_id"`
	// UsernameClaim optionally selects the actual host account name. An empty
	// value leaves account naming to the host's stable issuer/subject mapping.
	UsernameClaim string `json:"username_claim,omitempty"`
	// DisplayNameClaim selects a top-level string claim for presentation only.
	// Missing or unsuitable values fall back to the verified email.
	DisplayNameClaim string `json:"display_name_claim,omitempty"`
	// TraitClaims adds optional top-level claims to the default profile traits.
	// Provider credentials and reserved host identity traits cannot be exported.
	TraitClaims []string `json:"trait_claims,omitempty"`
	// RoleMappings map exact Zitadel project roles to existing Teleport roles.
	RoleMappings   map[string][]string `json:"role_mappings"`
	TransactionTTL time.Duration       `json:"-"`
	// MaxAuthenticationAge, when nonzero, requires a recent auth_time claim.
	MaxAuthenticationAge time.Duration `json:"-"`
	// RequiredAMR must all occur in the verified ID token. This does not assert
	// completion of Teleport's separate per-session MFA challenge.
	RequiredAMR []string `json:"required_amr,omitempty"`
	// PolicyRevision is a trusted host revision. Changing or recreating a
	// connector invalidates transactions from its previous revision.
	PolicyRevision string `json:"-"`
}

// ValidateConfig validates and copies trusted server-side configuration.
func ValidateConfig(cfg Config) (Config, error) { return cfg.validated() }

func (c Config) validated() (Config, error) {
	if err := httpsURL(c.IssuerURL); err != nil {
		return c, fmt.Errorf("issuer_url: %w", err)
	}
	u, _ := url.Parse(c.IssuerURL)
	if u.RawQuery != "" {
		return c, errors.New("issuer_url cannot contain a query")
	}
	if err := httpsURL(c.RedirectURL); err != nil {
		return c, fmt.Errorf("redirect_url: %w", err)
	}
	r, _ := url.Parse(c.RedirectURL)
	if r.RawQuery != "" {
		return c, errors.New("redirect_url cannot contain a query")
	}
	for name, value := range map[string]string{
		"client_id":  c.ClientID,
		"project_id": c.ProjectID, "organization_id": c.OrganizationID,
	} {
		if strings.TrimSpace(value) == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	if c.ClientSecret != "" && strings.TrimSpace(c.ClientSecret) == "" {
		return c, errors.New("client_secret must be empty or contain a non-whitespace value")
	}
	for _, id := range []string{c.ProjectID, c.OrganizationID} {
		if !identifier(id) {
			return c, errors.New("project and organization IDs must be numeric Zitadel IDs")
		}
	}
	if c.TransactionTTL == 0 {
		c.TransactionTTL = 5 * time.Minute
	}
	if c.TransactionTTL < time.Second || c.TransactionTTL > 10*time.Minute {
		return c, errors.New("transaction TTL must be between 1 second and 10 minutes")
	}
	if c.MaxAuthenticationAge < 0 || c.MaxAuthenticationAge%time.Second != 0 {
		return c, errors.New("maximum authentication age must be a nonnegative whole number of seconds")
	}
	if c.DisplayNameClaim == "" {
		c.DisplayNameClaim = "email"
	}
	if !claimName(c.DisplayNameClaim) {
		return c, errors.New("display name claim must be a top-level claim name without whitespace, at most 256 bytes")
	}
	if c.UsernameClaim != "" && !claimName(c.UsernameClaim) {
		return c, errors.New("username claim must be a top-level claim name without whitespace, at most 256 bytes")
	}
	if len(c.TraitClaims) > 32 {
		return c, errors.New("at most 32 additional trait claims are supported")
	}
	c.TraitClaims = slices.Clone(c.TraitClaims)
	for _, name := range c.TraitClaims {
		if !claimName(name) || strings.HasPrefix(name, "zitadel_") || slices.Contains([]string{
			"access_token", "refresh_token", "id_token", "client_secret", "code", "code_verifier", "nonce",
		}, name) {
			return c, errors.New("trait claims must be valid top-level names and cannot expose credentials or reserved identity data")
		}
	}
	slices.Sort(c.TraitClaims)
	c.TraitClaims = slices.Compact(c.TraitClaims)
	if len(c.RoleMappings) == 0 {
		return c, errors.New("at least one explicit role mapping is required")
	}
	// Take ownership of configuration so later caller mutations cannot change
	// the policy for outstanding login transactions.
	mappings := make(map[string][]string, len(c.RoleMappings))
	for role, targets := range c.RoleMappings {
		if !roleName(role) || len(targets) == 0 {
			return c, errors.New("role mappings require exact role names and nonempty target lists")
		}
		for _, target := range targets {
			if !roleName(target) {
				return c, errors.New("invalid Teleport role name in mapping")
			}
		}
		mappings[role] = slices.Clone(targets)
	}
	c.RoleMappings = mappings
	c.RequiredAMR = slices.Clone(c.RequiredAMR)
	for _, amr := range c.RequiredAMR {
		if !roleName(amr) {
			return c, errors.New("required AMR must contain nonempty exact values")
		}
	}
	return c, nil
}

func claimName(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func identifier(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func roleName(value string) bool {
	return value != "" && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "*?[]{}\r\n\t")
}

func httpsURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("an absolute HTTPS URL without credentials or fragment is required")
	}
	return nil
}
