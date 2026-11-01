# Lattice as Complement sees it: a container that listens on the two ports
# Complement reaches and answers for the server name it is given.
#
# The binary is built outside the image and copied in. That is not laziness: the
# image is rebuilt for every test run, and a homeserver that takes four minutes to
# compile inside every container is a suite nobody runs twice. It also means the
# image contains the compiler-free runtime, which is what you want shipped anyway.
#
# Build it with `make complement-image` (or `COMPLEMENT_BASE_IMAGE=... ` after
# `make build`). The binary must be static, so the runtime image needs no glibc.

FROM alpine:3.20

RUN apk add --no-cache openssl ca-certificates

COPY bin/lattice /usr/local/bin/lattice
COPY deploy/complement/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# 8008 is the client API port Complement calls, 8448 is the federation port it
# reaches over TLS. Both are the specification's defaults.
EXPOSE 8008 8448

VOLUME ["/data"]

ENTRYPOINT ["/entrypoint.sh"]
