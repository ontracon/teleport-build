# Local development

The labs run Teleport v18.10.0 Auth, Proxy, Web UI, and an SSH node. Published
ports bind to `127.0.0.1`. The mock lab also provides an HTTPS IdP with selectable
test identities. Run commands from the repository root.

## Mock lab

Prerequisites: Docker with BuildKit, Docker Compose v2, and Make.

```sh
make lab
```

This starts the mock environment, building the shared Debian-based integration
image if it is missing. Run `make lab-build` after changing source or build
files. Open [https://localhost:3080](https://localhost:3080),
choose **Zitadel Local Lab**, and select operator or administrator. Both may
access the local SSH node; administrator also has the `editor` role. Other IdP
buttons exercise denied cases.

Accept the generated localhost certificates at ports 3080 and 9443 in your
test browser. The adapter validates the IdP certificate against the private
lab CA. The CA and leaf certificates are valid for 30 days and remain in
ignored `dev/generated/`. No host trust store is changed.

```sh
make lab-down
docker compose -f dev/compose.yaml up -d --no-build --wait
```

Stopping preserves the mock cluster volume and generated configuration.
Compose project names remain `teleport-zitadel-lab` and
`teleport-zitadel-real-lab`, so the two environments retain separate data.

## Integration tests

With the mock lab running, install Node.js 18 or newer and browser dependencies:

```sh
make test-deps
# If Google Chrome is unavailable, install Playwright Chromium:
npx --prefix tests/integration playwright install chromium
make test-browser
make test-connectors
make test-cli
```

Browser scripts use Google Chrome's standard macOS location when present,
otherwise Playwright Chromium. Set `CHROME_PATH` for another Chrome executable.
Reports and screenshots go into ignored `tests/integration/test-results/`.

| Check | Coverage |
| --- | --- |
| `make test-browser` | Successful/denied login, readable labels, stable identity, callback replay, and CSRF |
| `make test-connectors` | Native YAML create/edit/delete, secret preservation, default selection, permissions, and stale revisions |
| `make test-cli` | `tsh` OIDC login, 30-minute certificates, node listing, and SSH |

For a shorter account-display check:

```sh
node tests/integration/browser-smoke.cjs --display-only
```

The integration image build also runs adapter and bridge race tests, selected
UI tests, and a production Web UI build. `make test` runs the independent
adapter's race tests and vet checks without starting Docker services.

With an existing upstream Git checkout, test the patch series and preparation
guards without modifying that checkout or fetching from the network:

```sh
make test-patches SOURCE_DIR=/path/to/teleport
```

The test creates a disposable sparse clone and retains its fixtures/logs under
ignored `tests/build/test-results/`. For editing upstream components, see
[patch maintenance](../integration/README.md).

## Real Zitadel

The real variant runs no mock IdP. It shares port 3080 with the mock lab, so
only one variant can run at a time.

For a new real environment, prepare the initial seed files:

```sh
mkdir -p dev/generated-real
chmod 700 dev/generated-real
cp oidc-adapter/config.example.json dev/generated-real/adapter.json
chmod 600 dev/generated-real/adapter.json
```

Edit `adapter.json` with your issuer, client ID, project ID, receiving
organization ID, exact role mappings, and callback
`https://localhost:3080/v1/webapi/oidc/callback`. Create
`dev/generated-real/client-secret` with the application secret and set its
permissions to `0600`. These files are gitignored. Configure the application
and role grants as described in [configuration](configuration.md).

```sh
make lab-down
make real-lab
```

Open https://localhost:3080 and complete the real account login and MFA in your
browser. The real service trusts public CA roots together with the localhost
CA. Its generated files remain in `dev/generated-real/`.

```sh
make test-real
```

This checks the browser handoff and configured role scopes. It accepts only
the generated localhost leaf key; real issuer TLS remains validated. It does
not enter user credentials or complete MFA.

Existing environments retain their cluster volumes and seed files. Native
connector resources are authoritative after initialization; manage subsequent
settings through the Web UI or `tctl`.

To switch back to the mock lab:

```sh
make real-lab-down
make lab
```

## Diagnostics

```sh
docker compose -f dev/compose.yaml logs --tail=50 teleport
docker compose -f dev/compose.real.yaml logs --tail=50 teleport
```

The browser shows a generic callback error. Auth logs include a safe stage,
reason, and provider HTTP status. `token_exchange` / `invalid_client` indicates
client authentication; `role_mapping` indicates a project, role, or organization
mismatch. A mapped role granted in another organization still denies access
and may report the relevant numeric grant organization IDs.

Each callback consumes its state, including failures. Start a fresh login after
correcting settings instead of refreshing a callback URL.

## Retained local material

The repository's structure cleanup moved the original research documents,
independent reference checkouts, and old caches into gitignored `deprecated/`.
It preserved generated credentials, certificates, browser reports, and cluster
volumes. `dist/` remains the ignored location for exported build artifacts.
