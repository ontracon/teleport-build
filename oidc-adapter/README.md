# Zitadel OIDC adapter

An independent Go module for authorization code flow, S256 PKCE, verified
OIDC identity, and exact Zitadel project/organization role policy. It has no
Teleport dependency. The [Teleport integration](../integration/) supplies
sessions, certificate issuance, permissions, and UI behavior.

For connector setup, use [configuration](../docs/configuration.md). For local
environments, use [development](../docs/development.md).

## Standalone checks

```sh
go test -race -count=1 ./...
go vet ./...
```

Tests use an in-memory HTTPS transport with real signed RSA JWTs. The mock IdP
under `cmd/mock-idp/` supplies the local browser/CLI fixture.

## Host contract

1. Construct a client from trusted configuration and a transaction store. Use
   a new client when policy or credentials change.
2. Pass the validated, serialized host login request to `Begin`. Retain its
   state, nonce, and PKCE verifier server-side.
3. Redirect the browser to `Authorization.URL` and pass its callback parameters
   to `Complete`. Consumption is atomic and precedes exchange, including failures.
4. Use the returned verified issuer/subject, optional account name, email,
   display label, traits, mapped roles, and original host request to perform host
   authorization and issue credentials. Provider tokens are never returned.

The host must preserve issuer/subject identity. `display_name_claim` selects
a top-level string claim for presentation, defaulting to `email`. Missing or
unsuitable labels fall back to verified email. `Identity.Email` remains the
verified email independently of `Identity.DisplayName`. Neither is a stable
account identifier. Optional `username_claim` supplies `Identity.Username`
for actual account naming. Unset leaves that field empty for host-controlled
naming. A configured claim must yield a valid account name or login fails.
The host must retain issuer/subject binding and reject account collisions,
including when another identity presents the same username.
UserInfo can fill missing fields only for the verified subject and cannot
override signed claims. OAuth/claim failures expose bounded, safe diagnostics
through `AuthenticationError`.

`Identity.Traits` contains verified `email`, `iss`, and `sub`, available profile
fields, and optional `trait_claims` from trusted configuration. Strings and
string lists are supported. Scalar booleans/numbers have explicit string
representations. Unsupported or oversized claims are omitted as a whole.
Token/session metadata can only come from the ID token. Other missing claims
may be filled by subject-matched UserInfo. Traits are rebuilt for every login.
See [user traits](../docs/configuration.md#user-traits) for defaults, limits,
reserved names, and optional lookup behavior.

`config.example.json` is the standalone and lab seed shape. `ReadConfig`
accepts an optional secret separately and rejects embedded secrets, unknown fields,
trailing JSON, and oversized input. Native Teleport resources supersede seed
configuration once the integration is initialized.

## Stores and transport

`MemoryStore` is bounded and intended for development. `BackendStore` requires
a shared `AtomicBackend`, uses compare-and-swap with expiring tombstones, and
prevents callback replay across replicas. The Teleport overlay binds that
contract to its backend.

Issuer, callback, and discovery endpoints require HTTPS. Discovered endpoints
must remain on the issuer origin. Redirects are refused. Requests time out
within 15 seconds and provider response bodies are limited to 1 MiB. The client
uses RS256 signatures and S256 PKCE. An empty client secret selects
Authentication Method **None**, sending the client ID and PKCE verifier in the
token request body without client authentication. A supplied secret selects
HTTP Basic authentication alongside PKCE. Token errors do not trigger a retry
with another authentication method. See
[application settings](../docs/configuration.md#zitadel-application).

See [architecture](../docs/architecture.md) for the integration, revision
binding, supported features, and remaining acceptance work.
