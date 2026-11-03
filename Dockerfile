# syntax=docker/dockerfile:1

# Lattice as a release image: one static binary, one directory to keep, and nothing
# else to install. There is no entrypoint script here, because there is nothing for
# one to do — every setting is an environment variable the server reads itself
# (internal/config/env.go), so the binary is the entry point and the environment is
# the configuration.
#
# This is the image .github/workflows/image.yml publishes when a tag is pushed;
# `make image` builds the same file locally. The version it reports comes from
# internal/service/capabilities.go, which is also why the release job refuses a tag
# that does not match it.
#
# The Complement image in deploy/complement/Dockerfile is a different thing on
# purpose: the test suite rebuilds its image on every run and needs a shell to mint
# certificates, so that one copies in a binary built outside the image. This one is
# built once per release and has to carry no compiler.

# The builder is pinned to the machine doing the build, and cross-compiles for the
# platform the image is being assembled for (TARGETOS/TARGETARCH are supplied by
# BuildKit). That is what makes an arm64 image on an amd64 runner a compile rather
# than an emulated compile.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Modules first: the download layer survives every source edit, so a rebuild after a
# commit does not fetch the world again.
COPY go.mod go.sum ./
RUN go mod download

# Only the two trees the server is made of. internal/persist reaches SQLite through
# gormlite, which is pure Go, so there is no C toolchain in here and nothing to
# install alongside the binary.
COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags '-s -w' -o /out/lattice ./cmd/server

FROM alpine:3.23

# ca-certificates is not decoration: outbound federation is TLS, and a server that
# cannot verify a peer's certificate is a server whose peers refuse it.
RUN apk add --no-cache ca-certificates \
	&& addgroup -g 10001 lattice \
	&& adduser -u 10001 -G lattice -D -H lattice \
	&& mkdir -p /data \
	&& chown 10001:10001 /data \
	&& chmod 0750 /data

COPY --from=build /out/lattice /usr/local/bin/lattice

# The image's own defaults, both of them about where the server lives inside the
# container rather than about what it is. -server-name and -jwt-secret are left
# unset deliberately: those are the operator's to choose, and a homeserver that
# picks its own identity is a homeserver whose accounts nobody can use.
ENV LATTICE_ADDR=":8008" \
	LATTICE_DB="/data/lattice.db"

# Labels, so the registry page says which commit this is.
ARG REVISION=unknown
LABEL org.opencontainers.image.title="lattice" \
	org.opencontainers.image.description="A small Matrix homeserver: one Go binary, one SQLite file" \
	org.opencontainers.image.source="https://github.com/enimatek-nl/lattice" \
	org.opencontainers.image.revision="${REVISION}"

WORKDIR /data
USER 10001:10001

EXPOSE 8008

VOLUME ["/data"]

# The cheapest route that is up only when the API is: /_matrix/client/versions is
# public, needs no account, and is served by the same handler that answers everything
# else. It reads LATTICE_ADDR's default, so a container started with a different
# address should replace the check or drop it with --no-healthcheck.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
	CMD wget -q -O /dev/null http://127.0.0.1:8008/_matrix/client/versions

ENTRYPOINT ["/usr/local/bin/lattice"]
