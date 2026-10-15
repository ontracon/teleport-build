package zitadel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// AuthenticationError exposes server-defined stages and safe reason codes.
// It never retains provider response bodies, tokens, callback parameters, or
// raw verification errors. Hosts may log these fields for failed login diagnosis.
type AuthenticationError struct {
	Stage      string
	Code       string
	HTTPStatus int
	// RoleGrantOrganizations contains at most 16 numeric organization IDs from
	// verified claims for mapped roles in the configured project. This helps
	// diagnose grants whose receiving organization differs from the owner.
	RoleGrantOrganizations []string `json:"role_grant_organizations,omitempty"`
	cause                  error
}

func (e *AuthenticationError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("OIDC %s: %s (HTTP %d)", e.Stage, e.Code, e.HTTPStatus)
	}
	return "OIDC " + e.Stage + ": " + e.Code
}
func (e *AuthenticationError) Unwrap() error { return e.cause }

func oauthFailureCode(value string) string {
	switch value {
	case "invalid_client", "invalid_grant", "invalid_request", "unauthorized_client", "unsupported_grant_type", "invalid_scope", "access_denied", "server_error", "temporarily_unavailable", "login_required", "interaction_required", "consent_required":
		return value
	default:
		return "provider_request_failed"
	}
}

func tokenExchangeFailure(err error) (string, int) {
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) {
		return "provider_request_failed", 0
	}
	status := 0
	if retrieve.Response != nil {
		status = retrieve.Response.StatusCode
	}
	return oauthFailureCode(retrieve.ErrorCode), status
}

func tokenValidationFailure(raw string, err error) string {
	// The header is untrusted. Recognize only fixed algorithm names for useful
	// diagnostics; this parsing never changes signature validation or policy.
	if parts := strings.Split(raw, "."); len(parts) == 3 {
		if data, decodeErr := base64.RawURLEncoding.DecodeString(parts[0]); decodeErr == nil {
			var header struct {
				Algorithm string `json:"alg"`
			}
			if json.Unmarshal(data, &header) == nil {
				switch header.Algorithm {
				case "EdDSA", "ES256", "ES384", "ES512", "RS384", "RS512", "PS256", "PS384", "PS512", "HS256", "HS384", "HS512", "none":
					return "unsupported_signing_algorithm_" + header.Algorithm
				}
			}
		}
	}
	var expired *oidc.TokenExpiredError
	if errors.As(err, &expired) {
		return "token_expired"
	}
	// Library error text is inspected but never retained or logged.
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "issuer"):
		return "issuer_mismatch"
	case strings.Contains(message, "audience"):
		return "audience_mismatch"
	case strings.Contains(message, "signature"):
		return "signature_verification_failed"
	default:
		return "invalid_id_token"
	}
}
