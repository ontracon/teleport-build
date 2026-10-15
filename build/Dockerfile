# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25.11
ARG NODE_VERSION=24.16.0
ARG RUST_VERSION=1.94.0

FROM golang:${GO_VERSION}-bookworm@sha256:b96f24a8d7d010ea0acb9c3ba99064740f02b6b984612b28bd3c9c5ab9453e38 AS go-toolchain
FROM node:${NODE_VERSION}-bookworm@sha256:40ad9f3064e67d6860b4bc3fe1880b2953934fd6320ada990e45fe0efa6badd7 AS node-toolchain
FROM rust:${RUST_VERSION}-bookworm@sha256:365468470075493dc4583f47387001854321c5a8583ea9604b297e67f01c5a4f AS rust-toolchain

FROM go-toolchain AS toolchain
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
ARG RUST_VERSION
ARG PNPM_VERSION=11.3.0
ENV GOTOOLCHAIN=local \
    RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    RUSTUP_TOOLCHAIN=${RUST_VERSION} \
    PATH=/opt/teleport-build/bin:/usr/local/cargo/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    FIDO2=off \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         build-essential cmake clang libclang-dev llvm patch pkgconf \
         libelf-dev libpam0g-dev libssl-dev libpcsclite-dev zlib1g-dev libatomic1 \
         ca-certificates curl file git gzip tar xz-utils python3 openssh-client \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node-toolchain /usr/local/bin/node /usr/local/bin/node
COPY --from=node-toolchain /usr/local/lib/node_modules/npm/ /usr/local/lib/node_modules/npm/
COPY --from=rust-toolchain /usr/local/cargo/ /usr/local/cargo/
COPY --from=rust-toolchain /usr/local/rustup/ /usr/local/rustup/
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && npm install --global "pnpm@${PNPM_VERSION}" \
    && rustup target add wasm32-unknown-unknown
COPY --chmod=0755 build/bin/pnpm /opt/teleport-build/bin/pnpm

FROM toolchain AS source
ARG GO_VERSION
ARG NODE_VERSION
ARG RUST_VERSION
ARG PNPM_VERSION=11.3.0
ARG TELEPORT_VERSION=18.10.0
ENV TELEPORT_VERSION=${TELEPORT_VERSION}
COPY integration/upstream.commit /opt/teleport-build/upstream.commit
WORKDIR /src/teleport
RUN git init . \
    && git remote add origin https://github.com/gravitational/teleport.git \
    && git fetch --depth=1 origin "refs/tags/v${TELEPORT_VERSION}:refs/tags/v${TELEPORT_VERSION}" \
    && git checkout --detach "v${TELEPORT_VERSION}" \
    && test "$(git rev-parse HEAD)" = "$(cat /opt/teleport-build/upstream.commit)" \
    && test "$(awk '$1 == "GOLANG_VERSION" { print $3 }' build.assets/versions.mk)" = "go${GO_VERSION}" \
    && test "$(awk '$1 == "NODE_VERSION" { print $3 }' build.assets/versions.mk)" = "${NODE_VERSION}" \
    && test "$(awk '$1 == "channel" { gsub(/"/, "", $3); print $3 }' rust-toolchain.toml)" = "${RUST_VERSION}" \
    && test "$(node -p 'require("./package.json").packageManager.split("+")[0]')" = "pnpm@${PNPM_VERSION}"

# Static libbpf retains enhanced recording without imposing a host distro.
RUN libbpf_version="$(awk '$1 == "LIBBPF_VERSION" { print $3 }' build.assets/versions.mk)" \
    && mkdir -p /tmp/libbpf-source /tmp/libbpf-install \
    && curl --fail --location --retry 3 "https://github.com/libbpf/libbpf/archive/refs/tags/v${libbpf_version}.tar.gz" \
         | tar -xz --strip-components=1 -C /tmp/libbpf-source \
    && make -C /tmp/libbpf-source/src BUILD_STATIC_ONLY=y EXTRA_CFLAGS=-fPIC \
         LIBDIR=/usr/lib64 DESTDIR=/tmp/libbpf-install install install_uapi_headers \
    && mkdir -p "/usr/libbpf-${libbpf_version}" \
    && cp -a /tmp/libbpf-install/usr/. "/usr/libbpf-${libbpf_version}/" \
    && rm -rf /tmp/libbpf-source /tmp/libbpf-install

FROM source AS wasm-tools
ARG BUILD_JOBS=2
ENV BUILD_JOBS=${BUILD_JOBS} CARGO_BUILD_JOBS=${BUILD_JOBS}
RUN --mount=type=cache,id=teleport-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=teleport-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,id=teleport-cargo-git,target=/usr/local/cargo/git,sharing=locked \
    --mount=type=cache,id=teleport-debian-wasm-tools,target=/tmp/wasm-tools-target,sharing=locked \
    CARGO_TARGET_DIR=/tmp/wasm-tools-target make --no-print-directory ensure-wasm-deps FIDO2=off

FROM wasm-tools AS dependencies
RUN --mount=type=cache,id=teleport-pnpm-store,target=/root/.local/share/pnpm/store,sharing=locked \
    pnpm install --frozen-lockfile

FROM dependencies AS prepared
COPY oidc-adapter/*.go oidc-adapter/config.example.json /opt/zitadel-adapter/
COPY integration/ /opt/zitadel-integration/
COPY --chmod=0755 build/prepare-teleport-source.sh /opt/teleport-build/prepare-source.sh
RUN /opt/teleport-build/prepare-source.sh --source /src/teleport \
      --adapter /opt/zitadel-adapter --integration /opt/zitadel-integration

FROM prepared AS build
COPY build/build.sh build/verify-build.py /opt/teleport-build/
ENV TELEPORT_INTEGRATION_DIR=/opt/zitadel-integration
RUN --mount=type=cache,id=teleport-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=teleport-debian-go-build,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,id=teleport-pnpm-store,target=/root/.local/share/pnpm/store,sharing=locked \
    --mount=type=cache,id=teleport-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,id=teleport-cargo-git,target=/usr/local/cargo/git,sharing=locked \
    --mount=type=cache,id=teleport-debian-cargo-target,target=/src/teleport/target,sharing=locked \
    TELEPORT_COMMIT="$(cat /opt/teleport-build/upstream.commit)" bash /opt/teleport-build/build.sh

FROM build AS verified
COPY build/test.sh build/smoke-test.py /opt/teleport-build/
RUN --mount=type=cache,id=teleport-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=teleport-debian-go-build,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,id=teleport-cargo-registry,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,id=teleport-cargo-git,target=/usr/local/cargo/git,sharing=locked \
    --mount=type=cache,id=teleport-debian-cargo-target,target=/src/teleport/target,sharing=locked \
    bash /opt/teleport-build/test.sh \
    && GOMAXPROCS=${BUILD_JOBS} go test -p=${BUILD_JOBS} -race -count=1 ./lib/zitadeloidc \
    && GOMAXPROCS=${BUILD_JOBS} go test -p=${BUILD_JOBS} -race -count=1 -run '^TestZitadel' ./lib/auth \
    && GOMAXPROCS=${BUILD_JOBS} go test -p=${BUILD_JOBS} -race -count=1 -run '^TestZitadel|^TestNewUserContext' ./lib/web/ui \
    && GOMAXPROCS=${BUILD_JOBS} go test -p=${BUILD_JOBS} -race -count=1 -run '^TestZitadel' ./lib/web \
    && GOMAXPROCS=${BUILD_JOBS} go test -p=${BUILD_JOBS} -race -count=1 -run '^TestZitadel' ./lib/service \
    && pnpm exec jest --runInBand --runTestsByPath \
         web/packages/teleport/src/services/user/zitadel-display-name.test.ts \
         web/packages/teleport/src/components/UserMenuNav/UserMenuNav.test.tsx

FROM go-toolchain AS mock-idp
ARG BUILD_JOBS=2
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src/adapter
COPY oidc-adapter/ ./
RUN --mount=type=cache,id=teleport-go-mod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=teleport-debian-go-build,target=/root/.cache/go-build,sharing=locked \
    GOMAXPROCS=${BUILD_JOBS} go build -p=${BUILD_JOBS} -trimpath -ldflags '-w -s' -o /out/mock-idp ./cmd/mock-idp

FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587 AS runtime-base
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         ca-certificates tzdata libpam0g libelf1 libssl3 libstdc++6 libatomic1 libpcsclite1 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY --from=verified /out/bin/teleport /out/bin/tctl /out/bin/fdpass-teleport /usr/local/bin/
COPY --from=verified /out/LICENSE /out/manifest.json /usr/local/share/teleport/
EXPOSE 3080
ENTRYPOINT ["/usr/local/bin/teleport"]
CMD ["start", "--config=/etc/teleport.yaml"]

FROM runtime-base AS lab
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 openssl curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=verified /out/bin/tsh /usr/local/bin/tsh
COPY --from=mock-idp /out/mock-idp /usr/local/bin/mock-idp
COPY dev/setup.py tests/integration/cli-smoke.py /lab/
ENV SSL_CERT_FILE=/lab/generated/ca.pem
WORKDIR /lab

FROM scratch AS artifacts
COPY --from=verified /out/ /

FROM runtime-base AS runtime
