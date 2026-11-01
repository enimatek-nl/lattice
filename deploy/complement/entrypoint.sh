#!/bin/sh
# The container entrypoint Complement expects: Lattice under test, listening on
# the two ports Complement reaches, with a certificate it can validate.
#
# Complement hands every homeserver container a certificate authority at
# /complement/ca, and its federation tests reach peers over HTTPS because that is
# what the specification requires. The two things to do about that are signing a
# certificate for this server's name with that CA, and trusting the CA when this
# server calls out. Neither is a test-only shortcut: a real deployment behind a
# private CA does exactly the same two things.
set -eu

SERVER_NAME="${SERVER_NAME:-localhost}"
DATA_DIR="${LATTICE_DATA_DIR:-/data}"
CLIENT_ADDR="${LATTICE_ADDR:-:8008}"
FEDERATION_ADDR="${LATTICE_FEDERATION_ADDR:-:8448}"
DB_PATH="$DATA_DIR/lattice.db"

mkdir -p "$DATA_DIR"

TLS_ARGS=""
CA_ARGS=""
CERT=""
KEY=""
if [ -f /complement/ca/ca.crt ] && [ -f /complement/ca/ca.key ]; then
	CERT="$DATA_DIR/$SERVER_NAME.crt"
	KEY="$DATA_DIR/$SERVER_NAME.key"
	if [ ! -f "$CERT" ]; then
		# The certificate has to name the server, and name it in the subjectAltName:
		# a Go verifier stopped accepting a bare common name years ago, and a
		# certificate that does not name the server is a handshake failure that looks
		# like a federation bug.
		openssl genrsa -out "$KEY" 2048 >/dev/null 2>&1
		openssl req -new -sha256 -key "$KEY" -subj "/CN=$SERVER_NAME" \
			-out "$DATA_DIR/server.csr" >/dev/null 2>&1
		printf 'subjectAltName=DNS:%s\n' "$SERVER_NAME" >"$DATA_DIR/server.ext"
		openssl x509 -req -in "$DATA_DIR/server.csr" \
			-CA /complement/ca/ca.crt -CAkey /complement/ca/ca.key -CAcreateserial \
			-out "$CERT" -days 365 -sha256 -extfile "$DATA_DIR/server.ext" >/dev/null 2>&1
		rm -f "$DATA_DIR/server.csr" "$DATA_DIR/server.ext" "$DATA_DIR/ca.srl"
	fi
	CA_ARGS="-ca-cert /complement/ca/ca.crt"
	# Trust it system-wide as well, so anything else in the container that validates
	# certificates agrees with Lattice.
	cp /complement/ca/ca.crt /usr/local/share/ca-certificates/complement.crt
	update-ca-certificates >/dev/null 2>&1
else
	# Nobody handed us a certificate authority, so the federation port gets a
	# self-signed one. Peers will not trust it, which is the correct outcome: an
	# untrusted certificate is a refusal, and a refusal is what federation should do
	# rather than a silent fallback to no encryption at all.
	CERT="$DATA_DIR/$SERVER_NAME.crt"
	KEY="$DATA_DIR/$SERVER_NAME.key"
	if [ ! -f "$CERT" ]; then
		openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 365 \
			-keyout "$KEY" -out "$CERT" -subj "/CN=$SERVER_NAME" \
			-addext "subjectAltName=DNS:$SERVER_NAME" >/dev/null 2>&1
	fi
fi
if [ -f "$CERT" ] && [ -f "$KEY" ]; then
	TLS_ARGS="-tls-addr $FEDERATION_ADDR -tls-cert $CERT -tls-key $KEY"
fi

# LATTICE_ARGS carries whatever the person running the suite wants to add: a
# different room version default, a slower outbox, the rate limits switched off.
exec /usr/local/bin/lattice \
	-server-name "$SERVER_NAME" \
	-db "$DB_PATH" \
	-federation \
	-addr "$CLIENT_ADDR" \
	$TLS_ARGS \
	$CA_ARGS \
	${LATTICE_ARGS:-}
