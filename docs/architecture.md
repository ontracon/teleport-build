# Architecture

The independent [OIDC adapter](../oidc-adapter/) validates identity and role
policy. The [Teleport overlay](../integration/overlay/) supplies host integration
and UI behavior. Teleport remains responsible for users, resource permissions,
session limits, certificates, and audit events.

## Source integration

`build/prepare-teleport-source.sh` verifies the commit recorded in
`integration/upstream.commit`, checks the ordered Git patch series, and checks
new-file destinations before modifying the checkout. It applies the patches,
copies adapter source into `lib/zitadeloidc`, and installs new overlay files.
An unexpected revision, local edit to a patched file, or existing payload
destination fails preparation. `--check` performs preflight without writing.

The plugin registers Auth and Proxy handlers and injects `auth.OIDCService`
using `auth.Server.SetOIDCService`. Its module wrapper advertises the implemented
OIDC capability. Registration is unconditional in this custom build, including
Linux archives and container images. Without a configured connector, startup
requires no Zitadel credentials or provider connection.

The overlay contains new Go/UI files. Changes to existing upstream components
live in three patches for plugin registration, account display labels, and
connector management. Their order is explicit in `integration/patches/series`.
See [patch maintenance](../integration/README.md) for preparing and updating a
checkout.

## Login and identity

1. Teleport validates the browser or CLI request. The adapter stores the trusted
   host request, state, nonce, and S256 PKCE verifier server-side.
2. The browser authenticates at Zitadel. The callback selects its connector from
   the stored request and atomically consumes the transaction before exchange.
3. The adapter exchanges the code and verifies issuer, audience, signature,
   expiry, nonce, and applicable token/authentication bindings. UserInfo may
   fill missing claims only when its subject matches the ID token.
4. Exact configured project roles must contain grants for the configured
   organization. A verified email is required; provider tokens remain private.
5. The bridge checks the current connector revision, resolves Teleport roles,
   invokes host login hooks, and issues the session or certificates through
   Teleport's existing machinery.

The internal username derives from connector, issuer, and subject. The account
menu defaults to verified email; a connector can select another top-level
string claim with `zitadel/display-name-claim`. The adapter persists that
display label separately from the email trait. Changing email or display
labels does not change the immutable identity key.

## Transactions and connector revisions

`BackendStore` uses a shared atomic backend and expiring compare-and-swap
tombstones. Concurrent or repeated callbacks cannot consume the same state
twice. The bounded in-memory implementation is available for standalone tests.

Every login and callback reads the native connector directly from the Auth
backend. Cached provider clients are bounded and keyed by the connector's
policy fingerprint, including its revision and credentials. The bridge checks
the fingerprint again before issuing a session, rejecting edits during login.

## Connector management

Authenticated Proxy endpoints expose native Zitadel connector CRUD through the
caller's Teleport client. Existing resource permissions and admin MFA checks
apply. Responses redact secrets; update requests can retain the stored secret
through an explicit marker. Updates require the fetched backend revision.

Listing uses the authoritative authentication preference without modifying it.
Default changes persist as native resources. Deleting a default selects a
remaining supported connector or local authentication.

## Supported scope

| Implemented | Current boundary |
| --- | --- |
| Browser and `tsh` OIDC login | Zitadel, HTTP Basic client authentication, RS256, HTTPS, and S256 PKCE |
| Role mapping | Exact project, receiving organization, and role keys |
| Native connector editor | Create/edit/delete, secret preservation, and default selection |
| Account labels | Configurable string claim, verified email fallback, and immutable account ID |
| AMR and authentication age | Signed claims; separate Teleport SSO MFA challenges remain unsupported |

SAML, generic enterprise OIDC, the native connector test flow, and Teleport SSO
MFA challenge flows are not implemented.

Mock browser/CLI checks and a real Zitadel login have succeeded locally. The
shared container build applies the integration before upstream `make full`,
retaining Desktop, PAM, BPF, CGO, and the embedded session helper. The lab adds
test tools to the same server runtime. The manual archive-release workflow
uses the same verified build for its patched Linux artifacts and keeps native
Windows/macOS client builds.

Release publishing, role-removal behavior over session/certificate lifetimes,
locks and audit acceptance, logout propagation, multi-replica operation, and
upgrades remain acceptance work. Runtime images contain executables and shared
runtime libraries; compiler toolchains and source remain in build stages.
