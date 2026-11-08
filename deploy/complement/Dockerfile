# Lattice image for the Complement compliance harness.
#
# Complement's image contract (README, "Image requirements"):
#   - EXPOSE 8008 (Client-Server API, plain HTTP) and 8448 (federation/key API, HTTPS)
#   - accept the server name through the SERVER_NAME environment variable
#   - answer GET /_matrix/client/versions with 200 OK
#   - manage its own storage
#   - tolerate the entrypoint running more than once
#   - use "complement" as the shared-secret registration secret
#
# Two stages: a Go build stage and a small runtime. Nothing outside the module is
# copied in, so the image is reproducible from the repository alone.

FROM docker.io/library/golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first so the module cache survives source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/lattice ./cmd/lattice

FROM docker.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates && update-ca-certificates

COPY --from=build /out/lattice /usr/local/bin/lattice
COPY deploy/complement/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh

# Provenance, carried by both image paths so `scripts/complement-image.sh` can check either one the
# same way: it asks the image what tree it came from instead of believing the caller. See the note
# in Dockerfile.prebuilt for why a run needs that.
ARG REVISION=unknown
ARG TREE=unknown
RUN printf 'revision=%s\ntree=%s\nmode=container\ncompiler=in-build (deploy/complement/Dockerfile)\n' \
	"$REVISION" "$TREE" > /REVISION

# No VOLUME for /data, for the reason spelled out in Dockerfile.prebuilt: an anonymous volume per
# homeserver container, ~700 per suite, podman's lock table at 2048, and a run that reports the
# runtime's exhaustion as protocol failures (20261107l). The entrypoint creates /data; Complement
# stops and starts the same container, whose own storage survives that.

EXPOSE 8008 8448

# The binary ships a healthcheck subcommand, so the image needs no curl.
HEALTHCHECK --interval=5s --timeout=5s --start-period=5s --retries=5 \
	CMD ["/usr/local/bin/lattice", "healthcheck", "-addr", "127.0.0.1:8008"]

ENTRYPOINT ["/entrypoint.sh"]
