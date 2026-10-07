# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, records technician measurements, requests a
repeat measurement when tolerance is exceeded, and seals the final evidence
record after review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- IntermediateCheck: an intermediate (between-calibration) check for one device
  with a result, standard, performer, performance time, and validity deadline.
- VerificationCase: one bounded assurance activity for a device, with a nominal
  value, tolerance, current state, version, and assigned steward.
- Measurement: an observed value, computed deviation, acceptance decision,
  technician, timestamp, and the intermediate check it was performed under.
- EvidenceSeal: the immutable closing record for a reviewed case, including the
  selected accepted measurement and the check it relied on.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Record Intermediate Check

The steward sends `POST /devices/{id}/checks` with a result, standard,
performer, performance time, and validity deadline. The deadline must be
strictly after the performance time; otherwise the registration is rejected
inside the atomic commit and leaves no check, device pointer, or audit event.
A passing, currently in-force check becomes the device's single current check.
A failed check clears the current check; an already-expired pass is recorded on
the ledger for traceability but is never adopted.

### 3. Open Verification Case

The steward sends `POST /cases` with a device ID, nominal value, tolerance, and
owner. The service verifies that the device exists, is active, and has no
unfinished case. A valid request creates a case in `awaiting_measurement`.

### 4. Record Measurement

A technician sends `POST /cases/{id}/measurements`. The case must be
`awaiting_measurement` and the device must have a passed, unexpired
intermediate check. The service pins that check onto the measurement and
computes the signed deviation from the nominal value. An in-tolerance
measurement moves the case to `review_ready`; an out-of-tolerance measurement
moves it to `recheck_required`.

### 5. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance result.
Only `recheck_required` cases may move back to `awaiting_measurement`. The
service records the actor and reason in the audit trail. A new measurement
still requires a check that is in force at that time.

### 6. Seal Verification

The verifier sends `POST /cases/{id}/seal` after an accepted measurement. Only
`review_ready` cases may be sealed. The service selects the accepted
measurement with the smallest absolute deviation, freezes the check that
measurement was performed under, creates exactly one evidence seal, moves the
case to `sealed`, and rejects later mutations. Checks recorded after a
measurement never rewrite its pinned check or the sealed record.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state:

- `awaiting_measurement -> review_ready`
- `awaiting_measurement -> recheck_required`
- `recheck_required -> awaiting_measurement`
- `review_ready -> sealed`
- `sealed` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- Measurements are accepted only while a case is awaiting measurement.
- A tolerance must be positive.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.
- A check's validity deadline must be strictly after its performance time; an
  invalid registration commits nothing.
- A device has at most one check currently in force: a failed check clears the
  current check, and an already-expired pass is never adopted.
- A new measurement is possible only while the device has a passed, unexpired
  check, and every measurement pins that check id at record time.
- The pinned check of a recorded measurement is immutable; later check
  registrations must not rewrite measurement or evidence-seal history.
- The evidence seal freezes the check referenced by the selected measurement.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /devices/{id}/checks`
- `GET /devices/{id}/checks`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The seal check also races two concurrent
seal requests to prove that the repository mutation lock permits exactly one
seal. The check-link checks cover rejected invalid registrations, failed and
expired checks blocking new measurements, and measurement-to-check pinning
surviving later checks and sealing.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
