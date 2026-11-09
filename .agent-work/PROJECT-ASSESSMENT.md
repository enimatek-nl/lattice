# Project Assessment

## Classification

**A. GREENFIELD** — new independent Matrix homeserver implementation in Go.
Repository at start: empty branch (`chore: empty branch (fresh start)`) containing only `AGENTS.md`.

## Assignment (as given)

Build an independent Matrix homeserver in Go, specification-driven, with Complement-based
interoperability testing. First iteration: research the spec, inspect Complement, analyse
reference implementations, design an architecture that can hold Matrix event graphs, room
state, authorization, room versions, federation and persistence, then deliver a **runnable
Go homeserver foundation** with automated testing and Complement integration infrastructure,
and verify an independently testable first milestone.

## Environment (verified 2026-11-05)

| Item | Value |
| --- | --- |
| Go toolchain | `go1.26.10` (linux/amd64) |
| Container runtime | podman 5.8.9, `podman.socket` active, `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock` (Docker-compatible API) |
| Network | available (git clone + module proxy + spec site reachable) |
| Skills installed | `autonomous-development`, `backend`, `frontend` |
| Skills assumed but **not** installed | `devops-engineer`, `security-engineer`, `database-design`, `test-master`, `microservices-architect`, `performance-engineering`, `cryptography` (AGENTS.md §2 lists them as examples; they are **not present** — their concerns are handled directly, see DEC-011) |

## Skills applied

- `autonomous-development` (orchestrator, end-to-end workflow)
- `backend` (Go/stdlib/GORM conventions) — **with the project override from the assignment**:
  tests live in a dedicated `tests/` tree, not alongside implementation files.
  Other backend-skill conventions that conflict with the Matrix spec (JWT auth, session
  semantics, generic REST shapes) are overridden by AGENTS.md §6 "Matrix-specific exceptions".
- `frontend` — not applicable.

## Deliverables this iteration

1. Pinned specification version + requirement/compliance tracking.
2. Complement analysis + a reproducible Complement execution environment (podman).
3. Architecture that accommodates event graphs, state, auth rules, room versions, federation,
   persistence from the beginning (documented + package skeleton).
4. Stage 0/1 foundation implemented and tested: config, logging, HTTP listeners (client plain
   HTTP + federation HTTPS), graceful shutdown, storage layer, Matrix error model, canonical
   JSON + signing JSON + identifier grammar, signing keys, router with Matrix 404/405 semantics.
5. First milestone (M1) beyond the bare skeleton: identity layer end-to-end
   (registration/UIA, shared-secret admin registration, login, devices, logout, whoami,
   `/versions`, `/capabilities`, well-known, federation `/version`, keys endpoint) verified
   against real Complement tests.
6. Docker image meeting Complement's image contract + runner scripts.
7. Honest compliance matrix and progress records.

## Non-goals this iteration

Rooms, events, state resolution, sync, federation traffic (beyond `/version` and key
endpoints), media, E2EE key management, appservices, presence, receipts, admin APIs beyond the
shared-secret registration endpoint used by Complement. These are architecture-aware but
unimplemented; recorded as NOT IMPLEMENTED in the compliance matrix.
