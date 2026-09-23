# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
#
# The reproducible build image for MCP packages.
#
# It carries the runtime lines at the same paths the MCP executor binds them,
# /opt/mcpgw/runtimes/<line>, and from the SAME upstream tarballs at the SAME
# versions and checksums MCPGW's deploy/docker/mcp-executor/Dockerfile uses,
# so a package is built against exactly the interpreter it will run under. A
# node@20 package whose native addon was compiled against node 22 builds clean
# and dies on the first tool call, and that is the failure this layout exists
# to make impossible. Python comes from python-build-standalone rather than a
# source build for the same reason: it is the interpreter the executor binds.
#
# Everything is fetched by digest. build/runtimes.sha256 is the checksum list
# and is checked before anything is unpacked. Moving a version here is a
# library release: see docs/CURATION.md, "Moving the build image".
#
# Go comes from the base image, which pins the toolchain native packages are
# compiled with.
FROM golang:1.27.1-trixie@sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183

ARG TARGETARCH
ARG NODE22_VERSION=22.23.2
ARG NODE20_VERSION=20.20.2
ARG PYTHON_BUILD=20260901
ARG PY312_VERSION=3.12.14
ARG PY311_VERSION=3.11.16

COPY build/runtimes.sha256 /tmp/runtimes.sha256

# xz-utils unpacks the node tarballs. It comes from the base image's own
# pinned Debian release, the same way MCPGW's executor image installs it.
RUN apt-get update \
    && apt-get install -y --no-install-recommends xz-utils \
    && rm -rf /var/lib/apt/lists/*

# pin-check: allow -- not a FROM: every tarball below is verified against the
# checksum list before it is unpacked, and a mismatch fails the build.
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) NODE_ARCH=x64; PY_ARCH=x86_64 ;; \
      arm64) NODE_ARCH=arm64; PY_ARCH=aarch64 ;; \
      *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    mkdir -p /opt/mcpgw/runtimes /tmp/rt; \
    fetch() { \
      url="$1"; line="$2"; file="${url##*/}"; \
      grep -F "  $file" /tmp/runtimes.sha256 > "/tmp/rt/$file.sha256"; \
      test -s "/tmp/rt/$file.sha256"; \
      curl -fsSL "$url" -o "/tmp/rt/$file"; \
      (cd /tmp/rt && sha256sum -c "$file.sha256"); \
      mkdir -p "/opt/mcpgw/runtimes/$line"; \
      tar -xf "/tmp/rt/$file" -C "/opt/mcpgw/runtimes/$line" --strip-components=1 --no-same-owner; \
      rm "/tmp/rt/$file"; \
      test -d "/opt/mcpgw/runtimes/$line/bin"; \
    }; \
    fetch "https://nodejs.org/dist/v${NODE22_VERSION}/node-v${NODE22_VERSION}-linux-${NODE_ARCH}.tar.xz" "node@22"; \
    fetch "https://nodejs.org/dist/v${NODE20_VERSION}/node-v${NODE20_VERSION}-linux-${NODE_ARCH}.tar.xz" "node@20"; \
    fetch "https://github.com/astral-sh/python-build-standalone/releases/download/${PYTHON_BUILD}/cpython-${PY312_VERSION}+${PYTHON_BUILD}-${PY_ARCH}-unknown-linux-gnu-install_only_stripped.tar.gz" "python@3.12"; \
    fetch "https://github.com/astral-sh/python-build-standalone/releases/download/${PYTHON_BUILD}/cpython-${PY311_VERSION}+${PYTHON_BUILD}-${PY_ARCH}-unknown-linux-gnu-install_only_stripped.tar.gz" "python@3.11"; \
    test -x /opt/mcpgw/runtimes/node@22/bin/node; \
    test -x /opt/mcpgw/runtimes/python@3.12/bin/python3; \
    chmod -R a-w,a+rX /opt/mcpgw/runtimes; \
    rm -rf /tmp/rt /tmp/runtimes.sha256

# The environment every step starts from. The builder sets the same values
# on each docker run, so a step behaves the same whether the image default or
# the builder's flag is the one that applies.
ENV GOTOOLCHAIN=local \
    GOFLAGS="-mod=readonly -modcacherw" \
    CGO_ENABLED=0 \
    SOURCE_DATE_EPOCH=0 \
    TZ=UTC \
    LC_ALL=C

WORKDIR /build/src
