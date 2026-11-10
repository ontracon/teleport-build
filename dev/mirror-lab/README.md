# Local Teleport mirror lab

This isolated lab exercises a custom Teleport control plane, an HTTPS static
mirror, systemd-based Linux agent containers and Managed Updates v2. It uses
real, verified amd64 Teleport binaries and archives, including the updater.
No binaries are mocked. Docker Desktop or a Linux Docker daemon, Compose v2,
Buildx, Python 3 and OpenSSL are required. Building Teleport requires substantial
time, disk space and memory. Amd64 emulation on an arm64 host is much slower.

```sh
make mirror-lab-build  # build and verify 18.10.0-10.1 and 18.10.0-11.1
make mirror-lab-up     # start the local control plane, mirror, and agents
make mirror-lab-test   # enroll hosts and exercise the managed updater
make mirror-lab-down   # stop services. Retain volumes and generated files
```

The two build numbers can be changed with `LAB_START_BUILD` and
`LAB_TARGET_BUILD` during the build. Set the matching `LAB_START_VERSION` and
`LAB_TARGET_VERSION` during the test. The target version must be compatible with
the control-plane version. The generated mirror files are immutable by name.
The publish helper rejects a different archive under an existing version. A
repeat build reuses existing artifacts only after checking their source,
integration, binary hashes and passing test results. Choose new build numbers
when those inputs change.

`build.sh` exports verified build outputs to `dist/mirror-lab/<build>/`, loads
the newer control-plane image directly from the verified binaries and copies
its two updater-compatible archives into
`dev/mirror-lab/generated/mirror/`. `setup.py` generates a private CA and TLS
certificates for the `teleport` and `mirror` Compose names. The agent image
trusts this CA and runs systemd. The mirror is reachable only through the
Compose network and localhost port 18443. The Proxy is exposed on localhost
port 13080. Lab agent containers require privileged mode and a writable cgroup
mount for systemd. They are isolated test hosts and should not be used to run
untrusted workloads.

The test expects a clean cluster and clean agent containers, and refuses to
reuse an existing managed-update version or installed agent. It checks missing
artifacts, mirror outages, bad checksums, direct and Web UI script enrollment,
discovery script selection and simulated delivery, conversion of an existing
agent to Managed Updates v2, groups, suffixes, bots and a rollout between the
two builds. It injects a failed service start to check the updater's revert and
host recovery, and verifies hashes of the binaries actually mapped by running
processes. A failed restart marks that version as broken in the upstream updater.
The runner uses `teleport-update enable` to explicitly retry it after recovery.
The discovery test executes the rendered
installer, intercepting its final cloud-specific configuration command and
checking the arguments. Cloud transport and identity are not emulated. The test
accelerates the updater by starting its normal systemd service. It does not
override the cluster's rollout decision. The lab also shortens the cluster's
rollout reconciliation interval to five seconds.

Logs are available with `docker compose -f dev/mirror-lab/compose.yaml logs`.
To deliberately discard this lab's cluster and host state for another clean run,
stop it and remove only the `teleport-mirror-lab` Compose project volumes and
containers. The CA and mirror assets under `generated/` remain until separately
removed. Do not run the destructive cleanup against another Compose project.
Native host, cloud provider, Kubernetes, Windows and macOS tests remain separate
release gates.
