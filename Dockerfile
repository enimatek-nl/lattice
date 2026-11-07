# syntax=docker/dockerfile:1

# Lattice as a release image: one static binary, one directory to keep, nothing to
# install alongside it. There is no entrypoint script here because there is nothing
# for one to do — every setting is a LATTICE_* environment variable the server reads
# itself (internal/config.ApplyEnv), so the binary is the entry point and the
# environment is the configuration. `make image` builds this file; the workflow in
# .github/workflows/image.yml publishes it when a tag is pushed.
#
# The Complement image in deploy/complement/Dockerfile is a different thing on
# purpose: the compliance harness rebuilds its image on every run and its entrypoint
# has to mint a certificate for the name the harness chose, so that one carries a
# shell. This one is built once per release and carries neither a shell script nor a
# compiler.
#
# Two listeners, and both belong in the image: 8008 is the Client-Server API over
# plain HTTP and 8448 is the Server-Server and key API over TLS. A homeserver that
# publishes no federation port is not a homeserver anyone else can talk to, so the
# federation listener is on by default (config.Federation.Enabled) and the TLS
# material it needs is generated under LATTICE_DATA_DIR when none is supplied.

# The builder is pinned to the machine doing the build and cross-compiles for the
# platform the image is assembled for (TARGETOS/TARGETARCH come from BuildKit). That
# is what makes an arm64 image on an amd64 runner a compile rather than an emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26-alpine AS build

WORKDIR /src

# Modules first: the download layer survives every source edit, so a rebuild after a
# commit does not fetch the world again.
COPY go.mod go.sum ./
RUN go mod download

# Only the two trees the server is made of. Storage goes through gormlite, which is
# pure Go, so CGO stays off: the result is a static binary and the runtime image
# needs no libc at all — which is also what keeps this image runnable on a musl base.
COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags "-s -w" -o /out/lattice ./cmd/lattice

FROM docker.io/library/alpine:3.20

# ca-certificates is not decoration: outbound federation is TLS, and a server that
# cannot verify a peer's certificate is a server whose peers refuse it.
RUN apk add --no-cache ca-certificates \
	&& addgroup -g 10001 lattice \
	&& adduser -u 10001 -G lattice -D -H lattice \
	&& mkdir -p /data \
	&& chown 10001:10001 /data \
	&& chmod 0750 /data

COPY --from=build /out/lattice /usr/local/bin/lattice

# The image's own defaults are about where the server lives inside a container, not
# about what it is. LATTICE_SERVER_NAME is left unset deliberately: a homeserver that
# picks its own identity is a homeserver whose accounts nobody can use, and the server
# refuses to run without one. The listen addresses are widened to 0.0.0.0 because the
# safe default (127.0.0.1) is unreachable from another container.
ENV LATTICE_DATA_DIR=/data \
	LATTICE_CLIENT_ADDR=0.0.0.0:8008 \
	LATTICE_FEDERATION_ADDR=0.0.0.0:8448

# Labels, so the registry page says which commit this is.
ARG REVISION=unknown
LABEL org.opencontainers.image.title="lattice" \
	org.opencontainers.image.description="An independent Matrix homeserver in Go: one binary, one SQLite file" \
	org.opencontainers.image.source="https://github.com/enimatek/lattice" \
	org.opencontainers.image.licenses="MIT" \
	org.opencontainers.image.revision="${REVISION}"

WORKDIR /data
USER 10001:10001

EXPOSE 8008 8448

VOLUME ["/data"]

# The cheapest check that is up only when the API is: /_matrix/client/versions is
# public, needs no account, and is served by the same router that answers everything
# else. The server ships a healthcheck subcommand that performs exactly that request
# and validates the answer, so the image needs no wget and no shell pipeline.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
	CMD ["/usr/local/bin/lattice", "healthcheck", "-addr", "127.0.0.1:8008"]

ENTRYPOINT ["/usr/local/bin/lattice"]
