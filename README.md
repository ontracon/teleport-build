# Teleport self-build with Zitadel

Build Teleport Community from pinned source and develop a custom Zitadel OIDC
integration. The integration supports browser and `tsh` login, exact project
role mappings, readable account labels, and connector management through
Teleport's Web UI.

The integration targets **Teleport v18.10.0**, commit
`ddaa46b8f4ee579d43480cd2d3b6a14b18e3ef7d`.

## Quick start

Install Docker with BuildKit/Buildx, Docker Compose v2, and Make. From the repository
root:

```sh
make lab
```

Open [https://localhost:3080](https://localhost:3080), accept the generated
localhost certificate, select **Zitadel Local Lab**, and choose a test identity.
The administrator can manage connectors under **Zero Trust Access → Auth
Connectors**. The first build compiles Teleport and its native dependencies.
Subsequent starts reuse the existing image. After source changes, run
`make lab-build` to rebuild it.

The mock and real labs share port 3080. If the real lab is running, stop it with
`make real-lab-down` before starting the mock lab. To use your own Zitadel
endpoint, follow [the real-lab setup](docs/development.md#real-zitadel).

```sh
make lab-down       # Stop services and retain cluster data and credentials
make help           # List build and test commands
```

## Configure Zitadel

Zitadel support is built in and enabled by default in this repository's server
binaries, Linux release archives, and container images. Start Teleport with
your normal server configuration, then create a native connector. A connector
and explicit role mappings are required before Zitadel can grant access.

To connect your Zitadel instance:

1. Register a Zitadel Web application with response type **Code**, grant type
   **Authorization Code**, and S256 PKCE. Authentication Method **None**
   works with the adapter. HTTP Basic client authentication is also supported.
   Set its redirect URL to
   `https://<teleport-proxy>/v1/webapi/oidc/callback`. Enable project roles in
   the ID token or UserInfo, and assign the intended project role to your user.
2. Sign in to Teleport as an administrator and open **Zero Trust Access →
   Auth Connectors → New Zitadel Connector**. Fill in the issuer, client
   credentials, project ID, receiving organization ID, and exact role mapping.
   Leave `client_secret` empty or omit it for Authentication Method **None**.
   For **Basic**, enter the Zitadel application secret. See
   [configuration](docs/configuration.md#connector-yaml) for the full YAML
   and the alternative `tctl` setup. Mapped Teleport roles must already exist.
3. Save the connector, use its tile menu to set it as the default if desired,
   and start a fresh login with Zitadel.

The account menu shows verified email by default. Set
`metadata.labels["zitadel/display-name-claim"]` to `preferred_username`, `name`,
or another string claim to change the label. See
[display label configuration](docs/configuration.md#account-display-label).

To use a claim as the actual Teleport username, set
`spec.username_claim: preferred_username` in the connector. Omitting it keeps
the default hashed account name. See [account naming](docs/configuration.md#account-username)
for claim requirements, collision handling, and existing accounts.

User traits include verified identity and available profile claims. Add
`metadata.labels["zitadel/trait-claims"]: '["groups", "amr"]'` to import
additional claims. See [user traits](docs/configuration.md#user-traits) for
supported values and claim precedence.

For a local test against your own endpoint, follow
[the real-lab setup](docs/development.md#real-zitadel).

## Build status

| Path | Output |
| --- | --- |
| `make build` | Debian slim runtime with patched Teleport, Web UI, and Zitadel |
| `make lab-build` | Both runtime and lab images. The lab adds setup, CLI tools, and mock IdP |
| `make artifacts` | All six compiled binaries, manifest, checksums, and reports |
| Manual release workflow | Patched Linux archives, a Helm-compatible GHCR server image, and native Windows/macOS clients |

Container builds and Linux release archives use the same verified Zitadel
integration build. Supported features and remaining acceptance work are documented in
[architecture](docs/architecture.md#supported-scope).

## Documentation

- [Configuration](docs/configuration.md): native connector YAML, role mappings,
  secret editing, and authentication defaults.
- [Development](docs/development.md): mock and real labs, tests, and diagnostics.
- [Building](docs/building.md): pinned toolchains, artifacts, and release CI.
- [Production containers](docs/containers.md): GHCR publication, official Helm chart values, and smoke tests.
- [Architecture](docs/architecture.md): extension hooks, identity, transactions,
  and supported scope.

## Repository layout

| Directory | Purpose |
| --- | --- |
| `oidc-adapter/` | Independent Go module for OIDC and Zitadel role policy |
| `integration/overlay/` | Teleport Auth/Proxy bridge, Web UI changes, and bridge tests |
| `integration/patches/` | Ordered Git diffs for changes to existing upstream files |
| `build/` | Source preparation, compilation, and artifact verification |
| `dev/` | Compose environments and local setup |
| `tests/integration/` | Browser and CLI checks against running labs |
| `docs/` | User and developer guides |
| `.github/workflows/` | Adapter tests and upstream build/release automation |

Build outputs, development credentials, dependency caches, browser reports,
and `deprecated/` are excluded from Git. Historical research and independent
reference checkouts are preserved locally in `deprecated/`. Active builds do
not depend on them.

## Adapter checks

With Go 1.24 or newer installed:

```sh
make test
```

For complete local browser and CLI checks, see
[integration testing](docs/development.md#integration-tests).

For upstream edits, see [maintaining the patch series](integration/README.md).
