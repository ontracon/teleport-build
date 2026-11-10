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

The release workflow attaches mirror assets to the GitHub release. It does not
upload them to an external mirror. Enrolling and updating hosts download the
archives directly, so they need network access to the mirror and must trust its
TLS certificate. Keep older releases available for rollback.

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
