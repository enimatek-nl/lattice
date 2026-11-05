# AGENTS.md — Go Matrix Homeserver (Lattice)

## 1. Project Mission

Build an independent Matrix homeserver implementation in Go.

The objective is to create a maintainable, secure, performant and interoperable Matrix server that implements the Matrix protocol according to its official specifications.

This is a new implementation, not a fork of Synapse, Dendrite or Complement.

The server must be designed to support:

- Matrix Client-Server API.
- Matrix Server-Server API (Federation).
- Matrix room and event semantics.
- Distributed room state.
- Event persistence and history.
- User and device management.
- Authentication and authorization.
- Matrix interoperability and compliance.

The ultimate objective is broad Matrix protocol compatibility.

Do not claim full Matrix compliance until it has been demonstrated through specification coverage and interoperability testing.

---

## 2. Mandatory Instructions and Skills

Before starting substantial work:

1. Read this AGENTS.md completely.
2. Load the `autonomous-development` skill.
3. Load the `backend` skill.
4. Discover additional relevant skills.
5. Read the relevant Matrix specifications.
6. Inspect the Complement repository and documentation.

Use specialist skills when relevant.

Examples:

- backend
- database-design
- security-engineer
- test-master
- devops-engineer
- microservices-architect
- performance-engineering
- cryptography

Do not assume these skills exist. Discover them first.

### Skill precedence

Follow this precedence:

1. Matrix specification: protocol requirements.
2. This AGENTS.md: project-specific architectural requirements.
3. Specialist skills: implementation and engineering guidance.
4. Existing project conventions: established implementation decisions.

If two instructions conflict, identify the conflict and document the decision.

Matrix protocol requirements always take precedence over generic backend conventions.

---

## 3. Authoritative References

### 3.1 Matrix Specification

Primary reference:

https://spec.matrix.org/latest/

Use the official Matrix specification as the authoritative source for protocol behavior.

Relevant sections include:

- Matrix architecture
- Client-Server API
- Server-Server API
- Room versions
- Event structure
- Event authorization
- Room state
- State resolution
- Federation
- Signing JSON
- Cryptographic requirements
- Device management
- End-to-end encryption
- Media repository

Pin the stable specification release selected for the project.

Record:

- Specification version.
- Publication date.
- Review date.
- Relevant sections.
- Implemented requirements.
- Outstanding requirements.

Do not use unstable specifications as normative implementation requirements.

Unstable specifications may be researched separately and documented as future work.

### 3.2 Matrix Complement

Reference repository:

https://github.com/matrix-org/complement

Complement is the primary external interoperability and compliance testing reference.

It is a black-box integration test suite for Matrix homeservers.

The implementation must be designed to run Complement against the new homeserver.

Important rules:

- Inspect the repository before deciding how to integrate it.
- Understand its test structure and configuration.
- Identify applicable tests.
- Identify the required homeserver deployment interface.
- Maintain a reproducible Complement execution environment.
- Run relevant tests throughout development.
- Track unsupported tests explicitly.
- Do not modify Complement tests to make the homeserver appear compliant.

The target is to pass applicable tests without weakening their assertions.

Complement tests are not a replacement for the Matrix specification.

Passing Complement does not establish complete Matrix compliance.

Also inspect Complement Crypto if end-to-end encryption interoperability becomes part of the project scope.

### 3.3 Other Matrix Implementations

You may inspect:

- Synapse
- Dendrite
- Conduit / conduwuit
- Other open-source Matrix implementations

Use them as implementation references, not as normative specifications.

Never assume that another implementation's behavior is automatically correct.

When implementations disagree, investigate the specification and document the conclusion.

---

## 4. Specification-Driven Development

Every significant protocol feature must be traceable to its specification.

Create a requirement tracking system.

Each requirement should have:

- Unique ID.
- Specification section.
- Description.
- Requirement level (MUST, MUST NOT, SHOULD, MAY).
- Implementation status.
- Test coverage.
- Known limitations.

Example:

REQ-CS-001:
Client authentication follows the Client-Server specification.

REQ-FED-001:
Federation requests are authenticated according to the Server-Server specification.

REQ-EVT-001:
Events are validated according to their applicable room version.

REQ-STATE-001:
Room state follows the applicable state resolution algorithm.

REQ-TEST-001:
The implementation is tested using applicable Complement tests.

Never treat SHOULD and MAY requirements as MUST requirements.

Do not silently omit normative requirements.

If a requirement cannot be implemented yet, record it explicitly.

Maintain:

- docs/specification-version.md
- docs/requirements.md
- docs/compliance-matrix.md

---

## 5. Project Architecture

The server must have clear separation between:

1. HTTP transport.
2. Client-Server API.
3. Server-Server API.
4. Authentication.
5. Event processing.
6. Event authorization.
7. Room state.
8. State resolution.
9. Federation.
10. Persistence.
11. User and device management.
12. Media management.
13. Background processing.

Prefer modular packages with explicit responsibilities.

Avoid a monolithic HTTP handler implementation.

Avoid unnecessary distributed services during initial development.

Start with a modular monolith unless actual requirements justify another architecture.

### Suggested conceptual modules

- server
- config
- api/client
- api/federation
- auth
- users
- devices
- rooms
- events
- eventauth
- state
- federation
- signing
- keys
- sync
- media
- database
- workers

These are architectural suggestions, not mandatory package names.

Determine the final structure during architecture planning.

---

## 6. Go Development Standards

Follow the `backend` skill.

Use:

- Go standard library HTTP server.
- `http.NewServeMux` with method-prefixed routes.
- Context propagation.
- Explicit error handling.
- Repository abstractions where appropriate.
- Structured logging.
- Table-driven tests.
- Proper goroutine lifecycle management.
- `errgroup` for coordinated concurrent operations where appropriate.
- GORM and gormlite according to the backend skill, subject to verified compatibility and performance requirements.
- OpenAPI documentation where applicable.

### Important Matrix-specific exceptions

Generic backend conventions must not compromise protocol correctness.

Examples:

- Do not replace Matrix authentication with JWT.
- Do not change Matrix JSON structures to fit internal Go structs.
- Do not normalize event content in ways that change its canonical JSON representation.
- Do not assume every event has the same validation rules.
- Do not assume every room uses the same room version.
- Do not treat event ordering as simple database insertion order.
- Do not simplify federation authentication into ordinary API authentication.

The Matrix protocol is the contract.

Internal implementation decisions must preserve that contract.

---

## 7. Event and Room Architecture

Events and room state are foundational parts of Matrix.

Design these carefully before implementing substantial API functionality.

### Event handling

Support the concepts required by the selected room versions:

- Event types.
- Event IDs.
- Room IDs.
- Sender.
- State keys.
- Event content.
- Timestamps.
- Previous events.
- Auth events.
- Hashes.
- Signatures.
- Redactions.
- Event relationships.

Do not assume all event types are state events.

Do not assume all events contain the same fields.

Use room-version-aware event processing.

### Event authorization

Implement authorization as a dedicated subsystem.

Do not embed authorization rules directly into HTTP handlers.

Authorization must account for the applicable room version and required state.

### Event graph

Model room history as an event graph.

Preserve:

- Event references.
- Forward extremities.
- Backward extremities.
- Event dependencies.
- State snapshots.
- Auth chains.

Do not model Matrix history as a simple sequential message list.

### Room state

Implement state management separately from event storage.

State resolution must follow the algorithm applicable to the room version.

Never substitute a simplified last-write-wins algorithm for Matrix state resolution.

### Room versions

Implement room-version-specific behavior.

Do not hardcode one room version throughout the application.

Maintain explicit room-version support and test coverage.

---

## 8. Federation Architecture

Federation is a core protocol capability, not an optional future HTTP integration.

Design federation with explicit separation from the Client-Server API.

Implement according to the Server-Server specification.

Important areas:

- Server name resolution.
- .well-known discovery.
- TLS.
- Server signing keys.
- Key publication.
- Key retrieval and caching.
- Request authentication.
- Transaction processing.
- PDU validation.
- EDU processing.
- Event signing.
- Event signature verification.
- Event authorization.
- Room joining.
- Backfilling.
- Missing event retrieval.
- State exchange.
- Federation retries.

Federation processing must be resilient to:

- Network failures.
- Unavailable remote homeservers.
- Invalid signatures.
- Missing events.
- Duplicate transactions.
- Duplicate events.
- Out-of-order event delivery.
- Remote server restarts.

Use bounded retries and appropriate timeouts.

Do not allow one unavailable remote server to block unrelated local operations.

Ensure background federation workers have controlled lifecycles and graceful shutdown.

---

## 9. Authentication and Security

Security must be considered from the beginning.

Follow Matrix-specific authentication requirements.

Implement appropriate support for:

- User authentication.
- Access tokens.
- Device sessions.
- Token invalidation.
- Password handling.
- Federation request authentication.
- Server signing keys.
- Key rotation.
- Authorization checks.

Never store plaintext passwords.

Never expose signing private keys.

Never log access tokens, passwords or private cryptographic material.

Validate untrusted input.

Apply appropriate request limits and resource limits.

Protect against:

- Unauthorized event injection.
- Forged federation requests.
- Replay attacks.
- Invalid event signatures.
- Resource exhaustion.
- Malformed JSON.
- Path traversal.
- SSRF through federation discovery.
- Unsafe URL redirects.
- Database injection.

Treat all incoming federation data as untrusted.

Do not trust a remote homeserver merely because it is part of the Matrix network.

Cryptographic implementations must use well-reviewed libraries and follow the specification precisely.

Do not invent cryptographic algorithms.

---

## 10. Database and Persistence

Design persistence around Matrix semantics.

The storage layer must support:

- Users.
- Devices.
- Access tokens.
- Rooms.
- Events.
- Event relationships.
- Event state.
- State snapshots.
- Membership.
- Room metadata.
- Server signing keys.
- Federation transactions.
- Sync data.
- Media metadata.

Maintain appropriate indexes for common queries.

Pay particular attention to:

- Event lookup.
- Room history.
- State lookup.
- Membership lookup.
- Sync position tracking.
- Federation transaction deduplication.

Use database transactions for operations that require atomic consistency.

Do not assume SQLite is sufficient for every deployment scenario.

Start with the backend skill's SQLite approach if appropriate, but keep storage boundaries clear and document scalability limitations.

Never sacrifice event consistency for convenience.

---

## 11. API Development

Implement Matrix APIs according to the pinned specification.

For each endpoint:

1. Identify the normative specification section.
2. Understand its HTTP method and path.
3. Understand authentication requirements.
4. Understand request parameters.
5. Understand request body.
6. Understand response format.
7. Understand error conditions.
8. Implement validation.
9. Add endpoint tests.
10. Add integration coverage.

Use the exact Matrix API paths and expected JSON formats.

Unsupported endpoints must return the protocol-defined errors.

Do not expose arbitrary internal error messages to clients.

Generate internal documentation as appropriate.

Do not assume conventional REST API practices override Matrix API requirements.

---

## 12. Testing Strategy

Testing is a mandatory part of development.

Use multiple testing levels.

### Unit tests

Test individual components:

- Event parsing.
- Event validation.
- JSON canonicalization.
- Signing and verification.
- Authorization.
- State calculations.
- Room-version rules.
- Database repositories.
- Authentication.
- Federation logic.

### Integration tests

Test interactions between:

- API handlers and services.
- Services and repositories.
- Event processing and persistence.
- Room state and event authorization.
- Federation and event processing.
- Sync and room history.

### Matrix protocol tests

Create tests derived directly from relevant specification requirements.

Use realistic Matrix protocol payloads.

Include valid and invalid examples.

### Complement tests

Run the official Complement test suite against the deployed homeserver.

Maintain:

- A reproducible Docker image.
- Complement configuration.
- Test execution scripts.
- Test result reports.
- Known unsupported test documentation.

Use focused tests during development and broader suites at milestones.

Never modify the official Complement assertions to hide implementation failures.

### Regression tests

Every discovered protocol bug should have a regression test.

Do not fix protocol bugs without adding coverage where practical.

---

## 13. Development Roadmap

Do not attempt to implement the entire Matrix protocol in one development cycle.

Create a staged roadmap.

The following is a suggested initial breakdown. Refine it after specification and Complement analysis.

### Stage 0 — Research and architecture

- Inspect the Matrix specification.
- Pin the specification version.
- Inspect Complement.
- Inspect existing Matrix implementations.
- Discover relevant development skills.
- Define project architecture.
- Define protocol scope.
- Create requirements and compliance tracking.
- Establish testing infrastructure.

Deliverable: architecture and implementation roadmap.

### Stage 1 — Server foundation

- Go project initialization.
- Configuration.
- Logging.
- HTTP server.
- Graceful shutdown.
- Database initialization.
- Database migrations.
- Health endpoint.
- Docker deployment.
- Basic discovery and version endpoints.

Deliverable: runnable homeserver foundation.

### Stage 2 — Users and authentication

- User registration.
- Login.
- Logout.
- Access tokens.
- Device management.
- User profiles.
- Authentication middleware.

Deliverable: basic usable homeserver identity layer.

### Stage 3 — Rooms and events

- Room creation.
- Room membership.
- Event creation.
- Event persistence.
- Basic event authorization.
- Room history.
- Room state.

Deliverable: basic messaging functionality.

### Stage 4 — Client synchronization

- Sync API.
- Timeline events.
- Membership changes.
- State updates.
- Pagination.
- Device synchronization.

Deliverable: basic Matrix client interoperability.

### Stage 5 — Federation foundation

- Server discovery.
- Server signing keys.
- Federation request authentication.
- Transactions.
- PDU validation.
- Event signing and verification.
- Basic federation exchange.

Deliverable: communication between independent homeservers.

### Stage 6 — Advanced room semantics

- Room versions.
- State resolution.
- Auth chains.
- Event redaction.
- Backfilling.
- Missing event retrieval.
- Restricted rooms.
- Knocking.
- Additional room functionality.

Deliverable: increasingly complete room protocol behavior.

### Stage 7 — Extended Matrix functionality

Evaluate and implement:

- End-to-end encryption-related server APIs.
- Device key management.
- Key queries and claims.
- Key backup.
- To-device messaging.
- Media repository.
- Push notifications.
- Application services.
- Presence.
- Receipts.
- Typing notifications.
- Room directories.
- Spaces.
- Additional optional APIs.

Deliverable: expanded ecosystem compatibility.

### Stage 8 — Compliance and hardening

- Expand Complement coverage.
- Resolve protocol incompatibilities.
- Security review.
- Performance testing.
- Load testing.
- Database optimization.
- Operational documentation.
- Deployment hardening.

Deliverable: documented and tested interoperability level.

Do not mark a stage complete based solely on code being present.

Require verification evidence.

---

## 14. Long-running Autonomous Development

Use the autonomous-development skill for substantial assignments.

Maintain persistent project state in:

.agent-work/

Required files:

- PROJECT-ASSESSMENT.md
- SPECIFICATION-VERSION.md
- REQUIREMENTS.md
- COMPLIANCE-MATRIX.md
- REFERENCE-ANALYSIS.md
- ARCHITECTURE.md
- PLAN.md
- TASKS.md
- ACCEPTANCE-CRITERIA.md
- PROGRESS.md
- BLOCKERS.md
- FINAL-REPORT.md

Update these files as the implementation evolves.

After context compaction or restarting:

1. Read AGENTS.md.
2. Read the autonomous-development skill.
3. Read the active plan.
4. Read the current task list.
5. Read the progress report.
6. Inspect the actual repository state.
7. Verify the last completed work.
8. Continue with the next incomplete task.

Do not trust progress records without checking actual code and test results.

Do not repeat completed tasks unnecessarily.

Do not stop after implementing the initial API functionality.

Continue until the current milestone's acceptance criteria are verified.

Use a bounded iteration budget and report blockers honestly.

---

## 15. Definition of Done

A task is complete only when:

- Its requirements are understood.
- Its implementation is complete.
- Its relevant tests pass.
- Relevant regression tests pass.
- The implementation follows the applicable Matrix specification.
- Documentation is updated.
- Progress records are updated.

A milestone is complete only when all mandatory milestone acceptance criteria are satisfied.

The overall project must not be described as Matrix-compliant without supporting evidence.

Maintain an honest compliance matrix.

Distinguish:

- Implemented.
- Tested.
- Complement verified.
- Partially supported.
- Not implemented.
- Not applicable.

Never confuse an implemented endpoint with a compliant implementation.

---

## 16. Final Engineering Principles

1. Protocol correctness before convenience.
2. Security before performance optimizations.
3. Correct event semantics before feature expansion.
4. Specification-driven development.
5. Explicit room-version support.
6. Test-driven protocol implementation.
7. Incremental architecture.
8. No speculative abstractions without a clear need.
9. No unnecessary dependencies.
10. No silent omission of requirements.
11. No fabricated test results.
12. No premature completion claims.

The ultimate objective is a real, interoperable Matrix homeserver implemented in Go.

Build it incrementally, verify it continuously, and preserve correctness throughout its development.

