package zitadel

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxTraitValueBytes = 1024
	maxTraitValues     = 128
	maxTraitBytes      = 32 << 10
)

func traitClaimNames(cfg Config) []string {
	names := append([]string{"preferred_username", "name", "given_name", "family_name", "nickname", "locale"}, cfg.TraitClaims...)
	slices.Sort(names)
	return slices.Compact(names)
}

// Authentication/session metadata must describe the verified ID token, never
// a similarly named custom UserInfo attribute.
func idTokenTrait(name string) bool {
	return slices.Contains([]string{"iss", "sub", "aud", "azp", "client_id", "sid", "at_hash", "amr", "acr", "auth_time", "iat", "exp", "nbf", "jti"}, name)
}

func missingTraitClaims(claims map[string]json.RawMessage, names []string) bool {
	for _, name := range names {
		if _, exists := claims[name]; !exists && !idTokenTrait(name) {
			return true
		}
	}
	return false
}

func identityTraits(claims map[string]json.RawMessage, names []string, issuer, subject, email string) map[string][]string {
	traits := map[string][]string{"email": {email}, "iss": {issuer}, "sub": {subject}}
	used := len("emailisssub") + len(email) + len(issuer) + len(subject)
	for _, name := range names {
		if _, protected := traits[name]; protected {
			continue
		}
		values, ok := traitValues(claims[name])
		if !ok {
			continue
		}
		size := len(name)
		for _, value := range values {
			size += len(value)
		}
		// Omit a whole claim when it cannot fit; never publish a truncated list.
		if used+size > maxTraitBytes {
			continue
		}
		traits[name] = values
		used += size
	}
	return traits
}

// Traits are optional: missing/null, structured, malformed, and oversized
// claims are omitted. Scalar booleans/numbers have explicit string encodings;
// lists must contain only valid strings, without partial conversion.
func traitValues(raw json.RawMessage) ([]string, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	valid := func(s string) bool {
		return strings.TrimSpace(s) != "" && len(s) <= maxTraitValueBytes && !strings.ContainsFunc(s, unicode.IsControl)
	}
	switch v := value.(type) {
	case string:
		if valid(v) {
			return []string{v}, true
		}
	case bool:
		return []string{strconv.FormatBool(v)}, true
	case json.Number:
		if valid(string(v)) {
			return []string{string(v)}, true
		}
	case []any:
		if len(v) == 0 || len(v) > maxTraitValues {
			return nil, false
		}
		values := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok || !valid(s) {
				return nil, false
			}
			values[i] = s
		}
		return values, true
	}
	return nil, false
}
