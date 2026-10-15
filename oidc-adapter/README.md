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
2. Pass the validated, serialized host login request to `Begin`; retain its
   state, nonce, and PKCE verifier server-side.
3. Redirect the browser to `Authorization.URL` and pass its callback parameters
   to `Complete`. Consumption is atomic and precedes exchange, including failures.
4. Use the returned verified issuer/subject, email, display label, mapped roles,
   and original host request to perform host authorization and issue
   credentials. Provider tokens are never returned.

The host must preserve issuer/subject identity. `display_name_claim` selects
a top-level string claim for presentation, defaulting to `email`. Missing or
unsuitable labels fall back to verified email. `Identity.Email` remains the
verified email independently of `Identity.DisplayName`; neither is a stable
account identifier.
UserInfo can fill missing fields only for the verified subject and cannot
override signed claims. OAuth/claim failures expose bounded, safe diagnostics
through `AuthenticationError`.

`config.example.json` is the standalone and lab seed shape. `ReadConfig`
requires the secret separately and rejects embedded secrets, unknown fields,
trailing JSON, and oversized input. Native Teleport resources supersede seed
configuration once the integration is initialized.

## Stores and transport

`MemoryStore` is bounded and intended for development. `BackendStore` requires
a shared `AtomicBackend`, uses compare-and-swap with expiring tombstones, and
prevents callback replay across replicas. The Teleport overlay binds that
contract to its backend.

Issuer, callback, and discovery endpoints require HTTPS. Discovered endpoints
must remain on the issuer origin; redirects are refused. Requests time out
within 15 seconds and provider response bodies are limited to 1 MiB. The client
currently supports HTTP Basic authentication and RS256 signatures.

See [architecture](../docs/architecture.md) for the integration, revision
binding, supported features, and remaining acceptance work.
