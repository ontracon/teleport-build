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
// against takeover. Email is display data and must not be the linking key.
type Identity struct {
	Issuer      string
	Subject     string
	Email       string
	DisplayName string
	Roles       []string
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
