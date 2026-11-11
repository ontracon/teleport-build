# Configure Teleport to use a CDN mirror

This guide covers the custom Community build based on Teleport 18.10.0. Replace
`cdn.example.com` and `18.10.0-10.1` with your mirror URL and a published build.

## Before deployment

Publish the image and the matching versioned archives and `.sha256` files to
the HTTPS mirror. For example, Linux amd64 agents must be able to download:

```text
https://cdn.example.com/teleport-v18.10.0-10.1-linux-amd64-bin.tar.gz
https://cdn.example.com/teleport-v18.10.0-10.1-linux-amd64-bin.tar.gz.sha256
```

The release workflow attaches mirror assets to the GitHub release and publishes
them to the configured Azure Blob container. Enrolling and updating hosts
download the archives directly, so they need network access to the mirror and
must trust its TLS certificate. Keep older releases available for rollback.

## Publish to Azure Blob Storage

Create the storage account and blob container before running a release. Create
the GitHub environment `azure-cdn`, then set `AZURE_STORAGE_ACCOUNT` and
`AZURE_STORAGE_CONTAINER` as environment or repository variables. Configure
environment protection rules and permitted branches to control who can publish.

Choose one authentication method:

- **OIDC:** Configure an Azure Entra application or user-assigned managed
  identity with a GitHub OIDC federated credential for this repository's
  `azure-cdn` environment. Grant it `Storage Blob Data Contributor` on the
  container. The built-in role also allows blob deletion. A custom role can
  restrict that permission. Set `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and
  `AZURE_SUBSCRIPTION_ID` as GitHub environment or repository variables. No
  Azure client secret or storage key is required.
- **Account key:** Add the Azure storage account access key as the GitHub
  environment secret `AZURE_STORAGE_KEY`. The workflow uses the key whenever
  this secret is present and skips Azure Login. The three OIDC variables are
  not required. Azure Shared Key access must be enabled on the storage account.
  The key grants broad access to the storage account, so use a dedicated
  account and rotate the key when needed.

To switch back to OIDC, remove the `AZURE_STORAGE_KEY` secret. The Azure
container and its network configuration must allow the
GitHub-hosted runner to upload. Every downloading host also needs access to the
mirror URL. Configure anonymous reads or an appropriate network-accessible
endpoint for those hosts. SAS query parameters are not supported in the mirror
base URL.

The release workflow first publishes the GitHub release, then publishes its
five mirror archives and their checksums to Azure. A failed Azure job leaves the
GitHub release available. To publish it later, run the **Publish Teleport release
to Azure mirror** workflow with the exact release tag, such as `v18.10.0-42.1`.
This workflow does not rebuild or create a release. It requires all five
mirror-compatible archive and checksum pairs, so older releases without those
assets cannot be published with it. All checksums are verified before upload.
Existing blobs are never replaced. A retry accepts an existing blob only when
its content matches, and finishes any remaining uploads. Publication is not
atomic, so point a cluster at a new version only after the job succeeds.

See [Microsoft's OIDC setup guide](https://learn.microsoft.com/en-us/azure/developer/github/connect-from-azure-openid-connect)
for creating a federated credential. The OIDC subject should match the
`azure-cdn` GitHub environment, not a branch-specific subject.

## Helm

Use the official `teleport-cluster` chart at the same upstream version as the
build, and add these values to your existing configuration:

```yaml
enterprise: false
image: ghcr.io/ontracon/teleport-build
teleportVersionOverride: "18.10.0-10.1"

# Only needed when the image is private. Create the secret in this namespace.
imagePullSecrets:
  - name: ghcr

extraEnv:
  - name: TELEPORT_CDN_BASE_URL
    value: "https://cdn.example.com"
```

The shared `extraEnv` applies to Auth and Proxy. If `auth.extraEnv` or
`proxy.extraEnv` is set separately, ensure that each includes the CDN variable.
Auth uses it for the built-in discovery installer. Proxy uses it when generating
installation scripts.

Apply the additions alongside your existing storage, TLS, routing, and
authentication values:

```sh
helm upgrade --install teleport teleport/teleport-cluster \
  --namespace teleport \
  --version 18.10.0 \
  -f current-values.yaml \
  -f cdn-values.yaml
```

The chart version is the upstream version. `teleportVersionOverride` selects
the matching custom image tag. See [production containers](containers.md) for
image publication and chart details.

## Docker Compose

For an existing configuration that runs Auth and Proxy together:

```yaml
services:
  teleport:
    image: ghcr.io/ontracon/teleport-build:18.10.0-10.1
    environment:
      TELEPORT_CDN_BASE_URL: https://cdn.example.com
    volumes:
      - ./teleport.yaml:/etc/teleport/teleport.yaml:ro
      - teleport-data:/var/lib/teleport
    ports:
      - "3080:3080"

volumes:
  teleport-data:
```

If Auth and Proxy run in separate containers, set the variable on both. Keep
the rest of your deployment configuration appropriate to your environment.

## Managed Linux agent updates

The CDN setting directs downloads. It does not enable managed updates. For a
new self-hosted cluster, the following example configures one default group and
an initial version. Inspect and merge existing resources if they are already
configured.

```yaml
# autoupdate-config.yaml
kind: autoupdate_config
version: v1
metadata:
  name: autoupdate-config
spec:
  agents:
    mode: enabled
    strategy: halt-on-error
    schedules:
      regular:
        - name: default
          days: [Mon, Tue, Wed, Thu, Fri, Sat, Sun]
          start_hour: 0
```

```yaml
# autoupdate-version.yaml
kind: autoupdate_version
version: v1
metadata:
  name: autoupdate-version
spec:
  agents:
    mode: enabled
    schedule: immediate
    start_version: 18.10.0-10.1
    target_version: 18.10.0-10.1
```

```sh
tctl create -f autoupdate-config.yaml
tctl create -f autoupdate-version.yaml
tctl autoupdate agents status
```

`immediate` permits updates immediately. Use a regular schedule and appropriate
groups for controlled production rollouts. For a new version, publish the image
and mirror artifacts first, upgrade the control plane to a compatible version,
then set the agent target version.

Existing Linux agents do not automatically adopt a changed mirror URL. Configure
their updater on each host:

```sh
sudo teleport-update enable \
  --proxy=teleport.example.com:443 \
  --base-url=https://cdn.example.com
```

Migration from a manually installed binary may require `--overwrite`. Check the
existing installation before using it. The updater persists the base URL in its
configuration. See [Teleport's agent update guide](https://goteleport.com/docs/upgrading/agent-managed-updates/)
for schedules, migration, status, and rollback.

## Client tools and platform scope

`tsh` and `tctl` use their own `TELEPORT_CDN_BASE_URL` environment variable for
client-tool updates. The cluster's environment does not configure workstations.
If the mirror uses a private CA, install trust on every downloading host. When
setting a custom CA bundle, include the other roots those applications need.

The local mirror lab verifies Linux amd64 enrollment and managed updates. The
macOS mirror-mode agent installer still needs changes and native launchd testing.
The presence of macOS archives alone does not enable that enrollment path.
Kubernetes agent images and Teleport Connect have separate distribution paths.
