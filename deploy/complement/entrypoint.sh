#!/bin/sh
# Container entrypoint for the Complement harness (and a reasonable starting point
# for any container deployment).
#
# Complement passes the homeserver's name in SERVER_NAME and mounts a certificate
# authority at /complement/ca/ca.{crt,key}. Signing our federation certificate
# with that CA is what makes the federation handshakes verifiable; the same shape
# applies to a real deployment behind a private CA. Lattice signs the certificate
# itself when LATTICE_TLS_CA_CERT_FILE/LATTICE_TLS_CA_KEY_FILE are set, so the
# image needs no openssl.
set -eu

SERVER_NAME="${SERVER_NAME:-localhost}"

export LATTICE_SERVER_NAME="$SERVER_NAME"
export LATTICE_DATA_DIR="${LATTICE_DATA_DIR:-/data}"
export LATTICE_CLIENT_ADDR="${LATTICE_CLIENT_ADDR:-0.0.0.0:8008}"
export LATTICE_FEDERATION_ADDR="${LATTICE_FEDERATION_ADDR:-0.0.0.0:8448}"
export LATTICE_LOG_LEVEL="${LATTICE_LOG_LEVEL:-info}"
export LATTICE_LOG_FORMAT="${LATTICE_LOG_FORMAT:-json}"

# Test-facing policy. Registration stays open so the harness can create accounts,
# and the shared secret is the one Complement signs. Bcrypt cost is at the minimum
# the server accepts: deliberately low for disposable containers, never a default
# for a real deployment.
export LATTICE_REGISTRATION_OPEN="${LATTICE_REGISTRATION_OPEN:-true}"
export LATTICE_REGISTRATION_SHARED_SECRET="${LATTICE_REGISTRATION_SHARED_SECRET:-complement}"
export LATTICE_BCRYPT_COST="${LATTICE_BCRYPT_COST:-4}"

# Outbound federation address policy for the harness. In the tests that involve a second
# homeserver, Complement *is* that homeserver: it runs a federation stub on the host machine at
# host.docker.internal, which resolves to an address on the Docker bridge network. This server's
# default policy declines to dial private ranges — that is the SSRF protection, not a mistake — so
# without this the tests cannot federate at all and the failure looks like a protocol bug. The image
# names the ranges the harness lives on instead of switching the policy off. The link-local range
# is there because Docker's embedded DNS answers host.docker.internal with a link-local address on
# some networks, which is the second homeserver these tests talk to.
export LATTICE_FEDERATION_IP_ALLOW="${LATTICE_FEDERATION_IP_ALLOW:-127.0.0.0/8,172.16.0.0/12,169.254.0.0/16}"

TLS_DIR="${LATTICE_TLS_DIR:-$LATTICE_DATA_DIR/tls}"
export LATTICE_TLS_CERT_FILE="${LATTICE_TLS_CERT_FILE:-$TLS_DIR/$SERVER_NAME.crt}"
export LATTICE_TLS_KEY_FILE="${LATTICE_TLS_KEY_FILE:-$TLS_DIR/$SERVER_NAME.key}"

mkdir -p "$LATTICE_DATA_DIR" "$TLS_DIR"

if [ -f /complement/ca/ca.crt ] && [ -f /complement/ca/ca.key ]; then
	export LATTICE_TLS_CA_CERT_FILE=/complement/ca/ca.crt
	export LATTICE_TLS_CA_KEY_FILE=/complement/ca/ca.key
	export LATTICE_TLS_AUTO_GENERATE=false
	# Also trust the CA system-wide, so anything in this container that validates
	# certificates agrees with Lattice.
	cp /complement/ca/ca.crt /usr/local/share/ca-certificates/complement.crt 2>/dev/null || true
	update-ca-certificates >/dev/null 2>&1 || true
else
	# No CA was supplied: the federation port gets a self-signed certificate. Peers
	# will reject it, which is the correct outcome for an unverifiable server.
	export LATTICE_TLS_AUTO_GENERATE=true
fi

exec /usr/local/bin/lattice
