package zitadel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Identity is verified authentication data, not a Teleport certificate request.
// Hosts must link users by issuer + subject and protect existing user records
// against takeover, including when Username supplies a claim-based account name.
type Identity struct {
	Issuer      string
	Subject     string
	Username    string
	Email       string
	DisplayName string
	Roles       []string
	// Traits contains optional profile/custom claims plus verified email, iss,
	// and sub. Hosts must preserve their own protected identity bindings.
	Traits map[string][]string
}

// usernameClaim accepts exact account names supported by Teleport's backend
// key sanitizer. Names are never trimmed, folded, or replaced with a fallback.
func usernameClaim(raw json.RawMessage) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 256 || value == "." || value == ".." {
		return "", ErrDenied
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("@_:.-+", ch) {
			continue
		}
		return "", ErrDenied
	}
	return value, nil
}

// displayName accepts plain provider text for presentation. It never selects
// the authentication identity or changes the verified email used by policy.
func displayName(raw json.RawMessage, email string) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return email
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsFunc(value, unicode.IsControl) {
		return email
	}
	return value
}

func projectRolesClaim(project string) string {
	return "urn:zitadel:iam:org:project:" + project + ":roles"
}

// mapRoles accepts both Zitadel's role object and its documented array of
// role objects. It never falls back to a different project's or generic roles.
func mapRoles(raw json.RawMessage, cfg Config) ([]string, error) {
	objects, err := roleObjects(raw)
	if err != nil {
		return nil, err
	}
	var roles []string
	for _, object := range objects {
		for role, orgs := range object {
			if _, granted := orgs[cfg.OrganizationID]; granted {
				roles = append(roles, cfg.RoleMappings[role]...)
			}
		}
	}
	slices.Sort(roles)
	roles = slices.Compact(roles)
	if len(roles) == 0 {
		return nil, fmt.Errorf("no mapped project role: %w", ErrDenied)
	}
	return roles, nil
}

func roleObjects(raw json.RawMessage) ([]map[string]map[string]string, error) {
	var objects []map[string]map[string]string
	trimmed := strings.TrimSpace(string(raw))
	switch {
	case strings.HasPrefix(trimmed, "{"):
		var object map[string]map[string]string
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, ErrDenied
		}
		objects = append(objects, object)
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal(raw, &objects); err != nil {
			return nil, ErrDenied
		}
	default:
		return nil, ErrDenied
	}
	for _, object := range objects {
		for role, orgs := range object {
			if role == "" || orgs == nil {
				return nil, ErrDenied
			}
			for org, domain := range orgs {
				if org == "" || domain == "" {
					return nil, ErrDenied
				}
			}
		}
	}
	return objects, nil
}

// Diagnostics reveal no domains, user identifiers, unrelated roles, or claims.
func mappedRoleGrantOrganizations(raw json.RawMessage, cfg Config) []string {
	objects, err := roleObjects(raw)
	if err != nil {
		return nil
	}
	var organizations []string
	for _, object := range objects {
		for role, orgs := range object {
			if len(cfg.RoleMappings[role]) == 0 {
				continue
			}
			for org := range orgs {
				if len(org) <= 20 && identifier(org) {
					organizations = append(organizations, org)
				}
			}
		}
	}
	slices.Sort(organizations)
	organizations = slices.Compact(organizations)
	if len(organizations) > 16 {
		organizations = organizations[:16]
	}
	return organizations
}
