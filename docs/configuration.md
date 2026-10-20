# Zitadel connector configuration

Zitadel support is enabled by default in this repository's patched server.
No activation environment variable is needed. A connector supplies the
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
  # Optional actual account name. Omit to keep the hashed default.
  # username_claim: preferred_username
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
| `zitadel/transaction-ttl` | Login transaction lifetime (default 5 minutes, maximum 10 minutes) |

For granted projects, the receiving organization can differ from the project
owner. A project grant ID in a console URL is a separate identifier. Only
matching project roles granted in the configured organization confer access.
Generic claims and wildcard role mappings are rejected.

## Zitadel application

The tested Zitadel application uses these settings:

| Setting | Value |
| --- | --- |
| Application Type | Web |
| Response Types | Code |
| Grant Types | Authorization Code |
| Authentication Method | None |
| PKCE | S256, supplied automatically by the adapter |
| Redirect URL | `https://<teleport-proxy>/v1/webapi/oidc/callback` |

HTTP Basic client authentication (`client_secret_basic`) is also supported,
but is not required for the tested **None + PKCE** setup. The adapter currently
sends a Basic authentication header in either case. With Authentication Method
**None**, Zitadel uses the client ID and validates PKCE without verifying the
supplied client secret. See
[Zitadel client verification](https://github.com/zitadel/zitadel/blob/main/internal/api/oidc/client.go)
and [PKCE token exchange](https://zitadel.com/docs/apis/openidoauth/endpoints#token_endpoint).

The adapter's local validation still requires a nonempty `spec.client_secret`.
For **None**, use a locally generated nonempty value in that field. It does
not need to be a Zitadel-issued secret. For **Basic**, use the actual
application secret issued by Zitadel. Omitting or emptying the field is
currently rejected by the adapter.

The callback must match the connector exactly. Configure Zitadel to return
project roles in the ID token or UserInfo, and assign the mapped role to the
user. The adapter requests the identity, project-audience, and role scopes
automatically.

## Editing secrets

The Web UI returns this marker in place of the stored secret:

```yaml
client_secret: "<existing-secret>"
```

Leaving the exact marker unchanged preserves the backend's secret when saving.
Replacing it updates the stored value. New connectors require a nonempty
value: use the actual Zitadel application secret for **Basic**, or a locally
generated value for **None**, as described in [application settings](#zitadel-application).
This behavior belongs to the custom Web API. Applying that marker through
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
`editor` role grants resource administration. `access` and the fixture's
`lab-access` role control resource access.

Accounts are bound to a connector, issuer, and subject. Their usernames default
to a hash of that identity. The optional naming policy below can use a claim
instead. The account menu uses a separate display label, with the actual
username as its fallback for accounts without a saved label.

## Account username

Use the native OIDC field to choose the actual Teleport account name:

```yaml
spec:
  username_claim: preferred_username
```

`email`, `sub`, and custom top-level string claims are supported too. The
selected value becomes the user resource name and the username used for
certificates, audit events, and username-based role templates. It is
independent of `zitadel/display-name-claim` and the verified `email` trait.

Omitting `username_claim`, or setting it to an empty string, preserves this
adapter's existing `zitadel-<hash>` naming. Unlike Teleport's native enterprise
OIDC connector, the default is not the email address. See
[Teleport's username claim documentation](https://goteleport.com/docs/zero-trust-access/sso/integrate-idp/oidc/#optional-specify-a-claim-to-use-as-the-username).

The claim name is an exact top-level JSON key. The ID token takes precedence.
A missing claim may come from UserInfo only when its subject matches the ID
token. A configured username claim is required: missing, empty, non-string,
or invalid values reject login, without falling back to email or a hash.
Names are case-sensitive and are never trimmed or normalized. They must be
at most 256 ASCII characters using letters, digits, `@`, `_`, `:`, `.`, `-`,
or `+`. `.` and `..` alone are rejected. These characters match Teleport's
backend key restrictions. A verified email is still required for login.

Usernames share the cluster-wide namespace. An existing user is updated only
when its recorded connector, issuer, and subject match the authenticated
identity. Local accounts, users from another connector or issuer, and other
subjects cannot be taken over by selecting the same claim value. Concurrent
creates and updates retain the backend's existence and revision checks.
The `zitadel_issuer` and `zitadel_subject` traits are reserved identity data
and are preserved after login-rule evaluation.

Enabling the field for an existing cluster, changing its selected claim, or
changing the claim value selects the corresponding account name on the next
SSO login. Existing accounts are not automatically renamed, merged, or
deleted. They retain their current expiry, and existing sessions and
certificates retain their lifetimes. New logins receive the roles mapped by
the connector at that time. Connector edits invalidate pending logins.

## Account display label

Choose the OIDC claim shown in the account menu with a connector label:

```yaml
metadata:
  labels:
    zitadel/display-name-claim: "preferred_username"
```

Omitting this label defaults to `email`. Common alternatives are `name` and
`nickname`. Custom top-level string claims are supported too. The claim name
is an exact JSON key, not a nested path or expression. The adapter already
requests the `profile` and `email` scopes. Your provider must return the
selected claim in the ID token or UserInfo.

The signed ID token takes precedence. A missing claim can be filled from
UserInfo only when its subject matches the verified ID token. Values are
trimmed and must be nonempty strings of at most 256 bytes with no control
characters. Otherwise the label falls back to verified email. A failed
UserInfo request also falls back to email when all required authentication
claims were already in the ID token. Subject mismatches are rejected. A
verified email is still required for login.

The selected label is saved with the OIDC identity on each successful login.
Changing it takes effect on the next SSO login. Editing the connector also
invalidates pending logins. It changes presentation only: the account ID,
email trait, role mappings, and certificate username retain their existing
meaning. Display labels need not be unique.

`spec.username_claim` selects the actual Teleport username as described
above. Use `zitadel/display-name-claim` to change presentation independently.

## User traits

Successful logins store verified `email`, `iss`, and `sub`, together with the
protected `zitadel_issuer` and `zitadel_subject` identity bindings. Available
`preferred_username`, `name`, `given_name`, `family_name`, `nickname`, and
`locale` claims are included automatically. Selecting `username_claim` or a
display label remains independent of exporting a claim as a trait.

Import additional top-level claims through a JSON array in a connector label:

```yaml
metadata:
  labels:
    zitadel/trait-claims: '["groups", "amr", "email_verified", "auth_time"]'
```

The standalone/lab seed equivalent is `"trait_claims": ["groups", "amr"]`.
This adds to the default traits. An empty list adds nothing. Names are exact
JSON keys, not nested paths or wildcards. At most 32 additional names are
accepted. Credential/transaction names such as `access_token`, `id_token`,
`refresh_token`, `client_secret`, `nonce`, and `code_verifier`, and names
starting with `zitadel_`, are rejected.

String claims become single-value traits and string arrays retain all their
values. Scalar booleans become `"true"` or `"false"`. Numbers retain their JSON
representation as strings, without floating-point rounding. Missing/null,
empty, structured, mixed-array, or malformed values are omitted. Values must
not contain control characters. Each optional value is limited to 1,024 bytes
and each array to 128 values. An optional claim is also omitted if adding its
name/values would take the claim-name/value payload beyond 32 KiB. Claims
exceeding a limit are omitted as a whole, never truncated. Verified identity
fields are retained.

The verified ID token takes precedence, including when a signed claim has an
unsuitable value. Missing profile/custom claims trigger one UserInfo request.
Its subject must match the ID token before any fields are merged. A failed
lookup may leave optional traits absent when the core identity, email, and
project-role checks are already complete. A subject mismatch rejects login.

Token/session claims such as `aud`, `azp`, `client_id`, `sid`, `at_hash`, `amr`,
`acr`, and `auth_time` can be selected explicitly, but are exported only from
the ID token. They are never filled from UserInfo, including when selected as
the username or display source. Missing claims are not fabricated. In
particular, `client_id` is not an automatic alias for `azp`.

`groups` must already be a flat claim supplied by Zitadel, for example through
a custom Action. The adapter does not flatten nested project-role objects or
map groups directly to Teleport roles. The configured project, receiving
organization, and exact role mappings still determine the assigned roles.
Imported traits can be referenced by role templates, such as
`{{external.preferred_username}}` or `{{external.groups}}`.

Traits are rebuilt on every successful login, so removed/missing claims do
not leave stale group membership on the stored user. Existing sessions and
certificates retain their issued traits until renewed. Login rules may
transform traits. `iss`, `sub`, `zitadel_issuer`, and `zitadel_subject` are
restored from the verified identity afterwards.

## Optional authentication policy

`zitadel/required-amr` accepts a JSON array of required authentication-method
values as a label string. Native `spec.max_age` is supported and requires a
sufficiently recent signed `auth_time`. These checks are distinct from
Teleport's SSO MFA challenge flow, which remains unsupported.

Additional scopes, disabled PKCE, unverified email,
unsupported OIDC provider settings, and unknown `zitadel/` labels are rejected.

## Bootstrap compatibility

The development setup uses an ignored `adapter.json` and a separate
`client-secret` to seed an empty cluster. Matching legacy connectors can be
migrated once into native resources. Once a connector is native, the adapter
uses its backend resource instead of the legacy files. Subsequent changes
belong in the Web UI or native `tctl` resource. Editing bootstrap files does
not update an initialized cluster.
