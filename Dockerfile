# syntax=docker/dockerfile:1.27@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

# soho base image: the soho CLI and skill, the claude, codex,
# grok and pi agent runner CLIs, and the Go/C/Git/Node toolchain a job
# container builds and tests with. Every input is pinned: the base image
# and the Dockerfile frontend by digest, Debian packages by an immutable
# snapshot.debian.org timestamp, downloaded archives by SHA-256, and npm
# packages by the checked-in lockfile in container/runners.
# docs/container-image.md documents the build, the reproducibility
# boundary, the runtime contract and how to update each pin.

ARG DEBIAN_IMAGE=docker.io/library/debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f
ARG DEBIAN_SNAPSHOT=20261005T000000Z

ARG BASE_IMAGE_VERSION=0.1.0
ARG SOHO_VERSION=dev
# Full commit SHA of the source the image was built from, recorded in the
# org.opencontainers.image.revision label. Empty when not passed.
ARG VCS_REF=

ARG GO_VERSION=1.27.2
ARG GO_SHA256_AMD64=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
ARG GO_SHA256_ARM64=94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8

ARG NODE_VERSION=24.21.0
ARG NODE_SHA256_AMD64=fd8e59d5a511510f6a298afb548f18c7d2b1be404d8b4a27d94fbe49f56cb2d6
ARG NODE_SHA256_ARM64=6ad1325edbdb5649c379b75a237147a666c95d4f9ae8d340fef2d1575d289ad2

# Claude Code native build; checksums from the release's official
# manifest.json (downloads.claude.ai/claude-code-releases/<version>/).
ARG CLAUDE_CODE_VERSION=2.1.296
ARG CLAUDE_CODE_SHA256_AMD64=24972e3bc859fab2b46ed4c1e51f7d6130f06d3bd550811a114640de3370d0de
ARG CLAUDE_CODE_SHA256_ARM64=f1f6e96e0d8342b9dbf41d7e88255397a6a52ce3d8736ad6a4c6b59c9b62fefa

# Grok CLI release artifact (x.ai/cli/grok-<version>-linux-<arch>). The
# upstream publishes no checksum file; these digests were recorded when the
# version was pinned (see docs/container-image.md, "Updating pins").
ARG GROK_VERSION=1.0.50
ARG GROK_SHA256_AMD64=c80de0155706ff2d995623887b102f2d0abd2c62b2643dea3a0cebaf7dd724d5
ARG GROK_SHA256_ARM64=947eea63e52393e00778d651d22e0dad22d30e9ee5b2af7944efc3fd4438ecc0

# npm-distributed runners; the exact trees come from
# container/runners/package-lock.json. Kept here for the smoke inventory.
ARG CODEX_VERSION=0.162.1
ARG PI_VERSION=1.1.0

# The build context exactly as this Dockerfile sees it, for inspection:
#   docker buildx build --target build-context --output type=local,dest=<dir> .
FROM scratch AS build-context
COPY . /

# Debian with apt pointed at the pinned snapshot and the shared packages.
FROM ${DEBIAN_IMAGE} AS os
ARG DEBIAN_SNAPSHOT
ENV LANG=C.UTF-8
RUN set -eu; \
    rm -f /etc/apt/sources.list.d/debian.sources; \
    printf '%s\n' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}" \
      'Suites: trixie trixie-updates' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
      '' \
      'Types: deb' \
      "URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}" \
      'Suites: trixie-security' \
      'Components: main' \
      'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
      > /etc/apt/sources.list.d/snapshot.sources; \
    apt-get -o Acquire::Check-Valid-Until=false -o Acquire::Retries=8 update; \
    DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=8 install -y --no-install-recommends \
      ca-certificates curl fd-find gcc git less libc6-dev openssh-client \
      patch procps ripgrep tini xz-utils; \
    ln -s /usr/bin/fdfind /usr/local/bin/fd; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/* /var/cache/debconf/*-old \
      /var/cache/ldconfig/aux-cache /var/lib/dpkg/*-old /var/log/*

# Verified downloads: Go, Node.js, Claude Code and Grok.
FROM os AS fetch
ARG TARGETARCH
ARG GO_VERSION GO_SHA256_AMD64 GO_SHA256_ARM64
ARG NODE_VERSION NODE_SHA256_AMD64 NODE_SHA256_ARM64
ARG CLAUDE_CODE_VERSION CLAUDE_CODE_SHA256_AMD64 CLAUDE_CODE_SHA256_ARM64
ARG GROK_VERSION GROK_SHA256_AMD64 GROK_SHA256_ARM64
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) go_arch=amd64; node_arch=x64; claude_arch=x64; grok_arch=x86_64; \
             go_sum=${GO_SHA256_AMD64}; node_sum=${NODE_SHA256_AMD64}; \
             claude_sum=${CLAUDE_CODE_SHA256_AMD64}; grok_sum=${GROK_SHA256_AMD64} ;; \
      arm64) go_arch=arm64; node_arch=arm64; claude_arch=arm64; grok_arch=aarch64; \
             go_sum=${GO_SHA256_ARM64}; node_sum=${NODE_SHA256_ARM64}; \
             claude_sum=${CLAUDE_CODE_SHA256_ARM64}; grok_sum=${GROK_SHA256_ARM64} ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    mkdir -p /dl /out/bin; cd /dl; \
    fetch() { curl -fsSL --proto '=https' --connect-timeout 30 --speed-limit 1024 --speed-time 60 --max-time 600 --retry 5 --retry-delay 10 --retry-max-time 1200 --retry-all-errors -o "$@"; }; \
    fetch go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${go_arch}.tar.gz"; \
    fetch node.txz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz"; \
    fetch claude "https://downloads.claude.ai/claude-code-releases/${CLAUDE_CODE_VERSION}/linux-${claude_arch}/claude"; \
    fetch grok "https://x.ai/cli/grok-${GROK_VERSION}-linux-${grok_arch}" \
      || fetch grok "https://storage.googleapis.com/grok-build-public-artifacts/cli/grok-${GROK_VERSION}-linux-${grok_arch}"; \
    printf '%s  %s\n' "${go_sum}" go.tgz "${node_sum}" node.txz "${claude_sum}" claude "${grok_sum}" grok | sha256sum -c -; \
    mkdir -p /out/go /out/node; \
    tar -xzf go.tgz -C /out/go --strip-components=1 --no-same-owner; \
    tar -xJf node.txz -C /out/node --strip-components=1 --no-same-owner; \
    install -m 0755 claude /out/bin/claude; \
    install -m 0755 grok /out/bin/grok; \
    rm -rf /dl

# soho, built from the curated source with the release flags.
FROM fetch AS soho
ARG SOHO_VERSION
ENV PATH=/out/go/bin:$PATH GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS=-mod=readonly \
    GOCACHE=/tmp/gocache GOPATH=/tmp/gopath
WORKDIR /src
COPY go.mod ./
COPY cmd/soho cmd/soho
COPY internal internal
COPY skills/soho skills/soho
RUN set -eu; \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/djalmajr/soho/internal/cli.version=${SOHO_VERSION}" \
      -o /out/soho ./cmd/soho; \
    mkdir -p /out/skill; \
    cd skills/soho; \
    cp -R SKILL.md README.md config.defaults roles references templates agents /out/skill/; \
    rm -rf /tmp/gocache /tmp/gopath

# npm-distributed runners (codex, pi) from the lockfile, without lifecycle
# scripts.
FROM fetch AS runners
ENV PATH=/out/node/bin:$PATH
WORKDIR /opt/soho/runners
COPY container/runners/package.json container/runners/package-lock.json ./
RUN set -eu; \
    npm ci --ignore-scripts --no-audit --no-fund --cache /tmp/npm-cache; \
    rm -rf /tmp/npm-cache /root/.npm

FROM os AS base
ARG BASE_IMAGE_VERSION
ARG SOHO_VERSION
ARG VCS_REF
ARG DEBIAN_IMAGE
ARG DEBIAN_SNAPSHOT
COPY --from=fetch /out/go /usr/local/go
COPY --from=fetch /out/node /opt/node
COPY --from=fetch /out/bin/ /usr/local/bin/
COPY --from=soho /out/soho /usr/local/bin/soho
COPY --from=soho /out/skill /usr/local/share/soho/skills/soho
COPY --from=runners /opt/soho/runners /opt/soho/runners
RUN set -eu; \
    ln -s /opt/soho/runners/node_modules/.bin/codex /usr/local/bin/codex; \
    ln -s /opt/soho/runners/node_modules/.bin/pi /usr/local/bin/pi; \
    groupadd --gid 1000 agent; \
    useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash agent; \
    install -d -o agent -g agent -m 0700 /home/agent/.claude /home/agent/.codex \
      /home/agent/.grok /home/agent/.pi; \
    mkdir -p /workspace; \
    chown agent:agent /workspace; \
    rm -rf /var/log/* /var/cache/ldconfig/aux-cache
ENV PATH=/usr/local/go/bin:/opt/node/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    GOTOOLCHAIN=local \
    SOHO_SKILL_DIR=/usr/local/share/soho/skills/soho \
    DISABLE_AUTOUPDATER=1 \
    GROK_DISABLE_AUTOUPDATER=1 \
    PI_SKIP_VERSION_CHECK=1
LABEL org.opencontainers.image.title="soho-base" \
      org.opencontainers.image.description="soho CLI and skill, agent runner CLIs and build toolchain for one-container-per-job execution" \
      org.opencontainers.image.version="${BASE_IMAGE_VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.base.name="${DEBIAN_IMAGE}" \
      io.github.soho.cli.version="${SOHO_VERSION}" \
      io.github.soho.debian.snapshot="${DEBIAN_SNAPSHOT}"
USER agent
WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
