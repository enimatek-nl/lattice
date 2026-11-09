# Reference Analysis

Inspected locally: `https://github.com/matrix-org/complement` (shallow clone at `/tmp/complement`,
HEAD `e3fcff1`, module `github.com/matrix-org/complement`, `go 1.26.0`).

## 1. Complement — what it is

Black-box integration test suite + harness for Matrix homeservers. It drives homeservers that run
**in containers** through the Docker API (`github.com/moby/moby/client v0.6.2`) and speaks Matrix
over HTTP with its own client library. It also contains a **mock remote homeserver**
(`federation/`) that receives/serves federation traffic so that outbound behaviour can be asserted
without a second real homeserver.

### Package layout (observed)

| Path | Role |
| --- | --- |
| `/` (`test_package.go`, `test_main.go`) | public API: `complement.Deploy(t, n)`, `TestPackage`, `Deployment` |
| `client/` | `CSAPI` client: `Do/MustDo`, `RegisterUser`, `RegisterSharedSecret`, `SyncUntil`, `GetCapabilities` |
| `internal/docker/` | `Deployer`, `Deployment`, image builder, container labels (`access_token_*`, `application_service_*`) |
| `b/` | blueprints: `HSWithApplicationService`, `OneToOneRoom`, `FederationOneToOneRoom`, `PerfManyMessages`, … |
| `federation/` | mock federation server (`server.go`, `server_room.go`, `handle.go`) |
| `match/`, `must/`, `should/` | declarative HTTP/JSON assertions |
| `helpers/` | `RegistrationOpts`, `TxnID`, `SyncUntil`-style waiters |
| `runtime/` | per-implementation skips: `synapse`, `dendrite`, `conduit`, `conduwuit`, `venator` |
| `tests/` | the actual suites: `tests/` (federation + top level) and `tests/csapi/` |
| `cmd/homerunner` | HTTP service that spins homeservers on demand (perf tooling) |
| `ENVIRONMENT.md` | generated reference of all `COMPLEMENT_*` env vars |

### Deployment contract (requirements imposed on our image)

From `README.md` ("Image requirements") + `internal/docker/deployer.go`:

- `EXPOSE 8008` (client, **plain HTTP**) and `EXPOSE 8448` (federation, **HTTPS**).
- Must accept the server name from env `SERVER_NAME` (deployer sets `SERVER_NAME=hs1`/`hs2`…).
- Must answer `GET /_matrix/client/versions` with 200 quickly; healthy within
  `COMPLEMENT_SPAWN_HS_TIMEOUT_SECS` (default 30s) if a `HEALTHCHECK` exists.
- Must manage its own storage inside the container; `CMD`/`ENTRYPOINT` may run multiple times.
- Registration shared secret for `/_synapse/admin/v1/register` must be `complement` if supported.
- PKI: Complement mounts `/complement/ca/ca.crt` + `/complement/ca/ca.key`; the homeserver must
  sign its own TLS certificate for `$SERVER_NAME` with that CA at each start and trust the CA
  (needed for federation TLS between containers).
- `COMPLEMENT_HOST_MOUNTS` supports bind mounts — used to avoid rebuilding images.
- Podman is supported via the Docker compatibility API:
  `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock BUILDAH_FORMAT=docker
  COMPLEMENT_HOSTNAME_RUNNING_COMPLEMENT=host.containers.internal`.

### How tests obtain users (behavioural evidence)

- `deployment.Register(t,"hs1",opts)` → `CSAPI.RegisterUser` unless admin:
  `POST /_matrix/client/v3/register` with `{username,password}` **must return 401 with a
  `session` and `flows[].stages`**, then the test retries with
  `auth: {session, type: "m.login.dummy"}`. So **open registration with an `m.login.dummy`
  flow is required** for almost every test to run at all.
- Password default `complement_meets_min_password_req`; admin users go through
  `/_synapse/admin/v1/register`: `GET` returns `{"nonce": ...}`, `POST` sends
  `{nonce,username,password,mac,admin}` where `mac = HMAC-SHA1(secret, nonce+"\0"+user+"\0"+pass+"\0"+("admin"|"notadmin"))`
  hex-encoded.
- Complement asserts user IDs like `@user-upper:hs1` → **usernames must be downcased**, and the
  server name is literally `hs1`.

### Test expectations relevant to M1 (read in full)

| Test | Requirement it imposes |
| --- | --- |
| `tests/csapi/apidoc_version_test.go::TestVersionStructure` | `/versions` → `versions` array of strings matching `v[1-9]\d*\.\d+(-x)?`, `unstable_features` must be an object if present |
| `tests/unknown_endpoints_test.go::TestUnknownEndpoints` | unknown path under `_matrix/{client,federation,key,media}` → **404 `M_UNRECOGNIZED`**; wrong method on a *known* endpoint (`PUT /_matrix/client/v3/login`, `PUT /_matrix/federation/v1/version`, `PUT /_matrix/key/v2/query`, `PATCH /_matrix/media/v3/upload`) → **405 `M_UNRECOGNIZED`** |
| `tests/csapi/apidoc_register_test.go::TestRegistration` | UIA flow listing; `m.login.dummy` registration; downcasing; `device_id` echo; invalid localparts → **400 `M_INVALID_USERNAME`**; duplicate → **400 `M_USER_IN_USE`**; localpart chars `q3._=-/` accepted; shared-secret admin + non-admin registration; non-ASCII passwords; `inhibit_login` |
| `tests/csapi/apidoc_login_test.go::TestLogin` | `GET /login` flow list; `m.login.password` with `identifier{type:m.id.user,user:<mxid or localpart>}`; `device_id` echo; unknown user rejected |
| `tests/csapi/apidoc_logout_test.go::TestLogout` | `POST /logout` invalidates that device only; `POST /logout/all`; `GET /devices` reflects session count |
| `tests/csapi/apidoc_server_capabilities_test.go::TestServerCapabilities` | `GET /_matrix/client/v1/capabilities` requires auth (401 unauthenticated) and contains `capabilities["m.room_versions"]` and `capabilities["m.change_password"]` |
| `tests/csapi/invalid_test.go::TestJson` | needs rooms/events → **M2 target**, not M1 |

### Notes / risks

- Complement tests are **not** a proxy for spec compliance (AGENTS.md §3.2) — they will be tracked
  separately from requirement coverage.
- Federation tests use the mock federation server: outbound requests from our container to
  `host.docker.internal` / `host.containers.internal` must not be firewalled.
- `runtime.Homeserver` allows per-implementation skips; unknown implementations (ours) get no
  skips, i.e. the strictest path.

## 2. Other implementations (survey, not normative)

| Implementation | What was taken from it | What was deliberately not taken |
| --- | --- | --- |
| Synapse (Python) | `/_synapse/admin/v1/register` shared-secret semantics that Complement depends on; error-code usage; UIA session model | Not a Go design reference; huge surface |
| Dendrite (Go) | Go monolith layout, `build/scripts/Complement.Dockerfile` approach for the complement image, shortname/`bigint` state-set optimisation idea | Its specific event-graph internals |
| gomatrixserverlib | Naming/vocabulary (PDU/EDU, `SplitID`, `verifyjson`, `signjson`, shortnames) | **Not used as a dependency** — the canonical-JSON/signing/event layers are implemented in-tree so the protocol surface stays independent and room-version-aware |
| conduwuit/Conduit (Rust) | Single-binary, config-file-driven deployment, `rocksdb`-style embedded storage | Storage engine not applicable |

Decision: **independent implementation** with stdlib + GORM/gormlite + golang.org/x/crypto only
(AGENTS.md §16 principle 9). `gomatrixserverlib` is a *reading* reference only.
