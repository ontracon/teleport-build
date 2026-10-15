# Zitadel connector configuration

Zitadel support is enabled by default in this repository's patched server;
no activation environment variable is needed. A connector supplies the
provider credentials and access policy.

Zitadel settings are native Teleport `oidc` resources with `spec.provider:
zitadel`. Create and edit them through **Zero Trust Access → Auth Connectors**
in the Web UI, or apply a resource with `tctl`. The Auth backend is authoritative:
edits affect new logins immediately and survive restarts.

## Connector YAML

Replace the example IDs, endpoint, credentials, and role key with your values.
The target Teleport roles must already exist.

```yaml
kind: oidc
version: v3
metadata:
  name: zitadel
  labels:
    zitadel/project-id: "123456789"
    zitadel/organization-id: "987654321"
    zitadel/transaction-ttl: "5m"
    zitadel/display-name-claim: "email"
spec:
  provider: zitadel
  display: Zitadel
  issuer_url: "https://zitadel.example.com"
  client_id: "<client-id>"
  client_secret: "<client-secret>"
  redirect_url: "https://localhost:3080/v1/webapi/oidc/callback"
  pkce_mode: enabled
  claims_to_roles:
    - claim: "urn:zitadel:iam:org:project:123456789:roles"
      value: "platform-admin"
      roles: ["access", "editor"]
```

The Web UI's [setup template](../integration/overlay/web/packages/teleport/src/AuthConnectors/templates/zitadel.yaml)
contains the same fields. Use your Proxy's public HTTPS address in the callback
for environments other than the localhost lab.

## Project and organization policy

| Setting | Meaning |
| --- | --- |
| `zitadel/project-id` | The project whose roles can grant Teleport access |
| `zitadel/organization-id` | The organization receiving the user's role assignment |
| `claims_to_roles[].value` | An exact Zitadel project role key |
| `claims_to_roles[].roles` | Existing Teleport roles granted for that key |
| `zitadel/transaction-ttl` | Login transaction lifetime; default 5 minutes, maximum 10 minutes |

For granted projects, the receiving organization can differ from the project
owner. A project grant ID in a console URL is a separate identifier. Only
matching project roles granted in the configured organization confer access;
generic claims and wildcard role mappings are rejected.

Register an application that supports HTTP Basic client authentication and
authorization code flow with S256 PKCE. The callback must match the connector
exactly. Configure Zitadel to return project roles in the ID token or UserInfo,
and assign the mapped role to the user. The adapter requests the identity,
project-audience, and role scopes automatically.

## Editing secrets

The Web UI returns this marker in place of the stored secret:

```yaml
client_secret: "<existing-secret>"
```

Leaving the exact marker unchanged preserves the backend's secret when saving.
Replacing it rotates the credential. New connectors require a real secret.
This behavior belongs to the custom Web API; applying that marker through
`tctl` does not preserve the existing credential.

The YAML returned by the editor includes a backend revision. Keep it when
saving: stale revisions are rejected. Connector changes also invalidate
pending logins, so start a fresh login after editing settings.

## Defaults and access

Use a connector tile's menu to set it as the default. Connector listing reads
the default without modifying authentication preferences. The lab seeds a
dynamic `cluster_auth_preference` so default selections persist across restarts.

Management uses the caller's Teleport resource permissions and the existing
admin MFA policy. Ordinary lab operators cannot edit connectors. A mapped
`editor` role grants resource administration; `access` and the fixture's
`lab-access` role control resource access.

Accounts retain an immutable connector/issuer/subject-based username. The
account menu uses the display label described below, with that internal name
as its fallback for accounts without a saved label.

## Account display label

Choose the OIDC claim shown in the account menu with a connector label:

```yaml
metadata:
  labels:
    zitadel/display-name-claim: "preferred_username"
```

Omitting this label defaults to `email`. Common alternatives are `name` and
`nickname`; custom top-level string claims are supported too. The claim name
is an exact JSON key, not a nested path or expression. The adapter already
requests the `profile` and `email` scopes; your provider must return the
selected claim in the ID token or UserInfo.

The signed ID token takes precedence. A missing claim can be filled from
UserInfo only when its subject matches the verified ID token. Values are
trimmed and must be nonempty strings of at most 256 bytes with no control
characters; otherwise the label falls back to verified email. A failed
UserInfo request also falls back to email when all required authentication
claims were already in the ID token; subject mismatches are rejected. A
verified email is still required for login.

The selected label is saved with the OIDC identity on each successful login.
Changing it takes effect on the next SSO login; editing the connector also
invalidates pending logins. It changes presentation only: the account ID,
email trait, role mappings, and certificate username retain their existing
meaning. Display labels need not be unique.

Teleport's native `spec.username_claim` selects the actual Teleport username.
This adapter rejects that field to preserve its stable account identity; use
`zitadel/display-name-claim` for presentation. See
[Teleport's username claim documentation](https://goteleport.com/docs/zero-trust-access/sso/integrate-idp/oidc/#optional-specify-a-claim-to-use-as-the-username).

## Optional authentication policy

`zitadel/required-amr` accepts a JSON array of required authentication-method
values as a label string. Native `spec.max_age` is supported and requires a
sufficiently recent signed `auth_time`. These checks are distinct from
Teleport's SSO MFA challenge flow, which remains unsupported.

Additional scopes, disabled PKCE, unverified email, `spec.username_claim`,
unsupported OIDC provider settings, and unknown `zitadel/` labels are rejected.

## Bootstrap compatibility

The development setup uses an ignored `adapter.json` and a separate
`client-secret` to seed an empty cluster. Matching legacy connectors can be
migrated once into native resources. Once a connector is native, the adapter
uses its backend resource instead of the legacy files. Subsequent changes
belong in the Web UI or native `tctl` resource; editing bootstrap files does
not update an initialized cluster.
