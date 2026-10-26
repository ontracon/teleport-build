# Production containers and the official Helm chart

The production image is built from the same verified Teleport Community source,
Zitadel integration, and embedded Web UI as the Linux release archives and local
labs. It is not an Enterprise or FIPS image.

## Image and release lifecycle

The manual [release workflow](../.github/workflows/build-linux-amd64.yml) builds
Linux artifacts and the production image in one Buildx Bake graph. It then tests
the production image, exports it as a checked Docker archive, and transfers that
exact image to the release job. There is no second compilation at publication.
Publication waits for the verified Linux build and all native client jobs.

The release job uses `GITHUB_TOKEN` with `packages: write` to publish to:

```text
ghcr.io/ontracon/teleport-build:18.10.0-build.<run>.<attempt>
```

The repository name follows the GitHub repository if this project is moved or
forked. The build number matches the numbered GitHub release. Each dispatch/run
attempt gets a separate tag; the workflow does not publish moving `latest` or
bare upstream-version tags. The binaries retain their upstream Teleport version.
An upstream version containing `+` has that character replaced by `-` in OCI
image tags only.

The publisher verifies the transported image ID, source revision label, upstream
version label, and Linux amd64 platform before tagging and pushing. Release notes
include the image tag, registry digest, and Helm values. GitHub release creation
and the registry push are not atomic: a later publication failure can leave a
numbered image without a completed GitHub release. Rerunning the workflow uses a
new attempt number.

### GitHub configuration

- The organization/repository must permit Actions to publish GHCR packages.
- New packages are private by default. Choose public visibility explicitly if
  anonymous pulls are intended; the workflow does not change package visibility.
- If the package already exists, ensure this repository has Actions write access
  in the package settings. The image's OCI source label links it to the repository.
- For a private package, provide a suitable GHCR pull credential to Kubernetes
  (for example, a classic PAT with `read:packages`, authorized for the organization
  as required). The ephemeral workflow token is for publication, not cluster pulls.

Only the production runtime is published. The development lab, compiler stages,
source, and test tools are not published as production images. Registry signing
and release SBOM generation are not configured yet.

## Official chart configuration

Use the official `teleport-cluster` chart from
`https://charts.releases.teleport.dev` at the **same upstream version** as our
build. For the current source pin, that is **18.10.0**, not the deployment's
previous 18.9.1 pin.

Merge these additions into the existing chart values, replacing the example
build number with a successfully published one:

```yaml
enterprise: false
image: ghcr.io/ontracon/teleport-build
teleportVersionOverride: "18.10.0-build.42.1"

# Required only for a private package. Provision this secret before Helm runs.
imagePullSecrets:
  - name: ghcr
```

`image` is a repository string, not an object with repository/tag keys. The chart
appends the tag from `teleportVersionOverride`. Here the override selects our
custom build of the matching upstream version; do not use it to bypass chart /
server version alignment. The chart's existing image settings are tag-based;
the digest in release notes is an audit/verification reference, not a digest-only
value that can be substituted into `teleportVersionOverride`.

For private pulls, create a `kubernetes.io/dockerconfigjson` secret in the Teleport
namespace before creating/upgrading the Helm release. Validation hooks need the
secret before the main workloads start. Manage the credential through the usual
secret-management tooling rather than committing it to a values file.

One custom image serves Auth, Proxy, both configuration-validation jobs, and the
Proxy upgrade-wait init container. If the operator is enabled for Machine ID bots,
keep its official image at the matching chart version. Do not set
`operator.image` to this server image. If private operator images are introduced
later, their pull-secret settings are separate subchart values.

The existing cluster name, storage configuration, certificates, Traefik routing,
and multiplex settings should be retained. Changing the image does not configure
Zitadel by itself: create a [native connector](configuration.md) and role mappings.
Do not use this Community build as a silent replacement for Enterprise deployments.

### Terraform handoff (`otc-code/cep-apps-new`)

The deployment changes are outside this build repository:

1. Change `teleport.tf`'s `chart_version` from `18.9.1` to `18.10.0`.
2. Expose custom image repository/build tag as deployment inputs and pass them to
   `templates/teleport.yaml` using the keys above. Keep official-image deployment
   available when the override is unset.
3. If private, provision the namespace-scoped GHCR pull secret and make the Helm
   application depend on it.
4. Require the Community edition when selecting this custom build.
5. Back up the Teleport backend/PVC and run a staging upgrade before production.

No fork or republication of the official chart is needed.

## Container launch contract

The production target alone uses:

```dockerfile
ENV TELEPORT_TOOLS_VERSION=off
STOPSIGNAL SIGQUIT
ENTRYPOINT ["/usr/bin/dumb-init", "/usr/local/bin/teleport", "start", "-c", "/etc/teleport/teleport.yaml"]
CMD []
```

Mount configuration read-only at `/etc/teleport/teleport.yaml` and persist
`/var/lib/teleport`. Chart arguments such as `--diag-addr=0.0.0.0:3000` are appended
to this complete invocation. `teleport` and `tctl` remain on `PATH`; the chart can
override the entrypoint for `teleport configure --test` and `teleport wait`.
`SIGQUIT` is forwarded through `dumb-init` for graceful shutdown.

The lab inherits `runtime-base`, not the final production target. It retains
`ENTRYPOINT ["/usr/local/bin/teleport"]` and its existing default config path.
`make lab`, `make real-lab`, and their explicit `start` commands are unchanged.
Both images still depend on the same verified binaries and Web UI.

## Local checks

Build only the production image, or export release artifacts and load the image
in the same graph:

```sh
make build
# or:
SOURCE_REVISION="$(git rev-parse HEAD)" make release-build
```

Docker, Helm 3 (CI uses 3.18.6), and Python 3 are needed for the production smoke
test. The image must already exist locally; the test does not rebuild it:

```sh
make test-runtime
# Test another local image:
make test-runtime RUNTIME_IMAGE=teleport-zitadel:my-build
# Reuse a downloaded chart without fetching it again:
python3 build/test-runtime.py --image teleport-zitadel:18.10.0 \
  --version 18.10.0 --chart /path/to/teleport-cluster
```

The smoke test checks:

- Production entrypoint, default arguments, tool-download setting, and stop signal.
- Image selection in the matching official chart's workloads, hooks, and init.
- Both rendered chart configurations through `teleport configure --test`.
- Actual Auth/Proxy startup using the default entrypoint and **flag-only** arguments,
  including the chart's apply-on-startup token resource.
- `/healthz`, `/readyz`, the version API, embedded Web UI HTML, and `tctl status`.
- The chart's wait subcommands and a clean `SIGQUIT` shutdown.

It uses temporary configuration/data, loopback-only dynamic ports, and a uniquely
named container that it removes on exit. No existing lab container or volume is
modified. This test is intentionally not a full Kubernetes deployment: Kubernetes
joining, operator reconciliation, ingress/TLS, PostgreSQL, production storage
permissions, and real upgrades still require staging acceptance tests.

Fast regressions do not need a running Docker daemon:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/build -p 'test_*.py' -v
```

Linux amd64 is the initial published platform. ARM64 publication requires its own
native build and verification; do not infer support from an amd64 smoke test under
emulation.
