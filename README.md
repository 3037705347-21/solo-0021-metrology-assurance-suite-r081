# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases,
records measurements, handles failed tolerance decisions, and seals traceable
evidence records.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow record-intermediate-check
go run ./cmd/workflowcheck --workflow measurement-check-gate
go run ./cmd/workflowcheck --workflow measurement-check-pinning
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add the unit and integration test suite and replace
or extend these smoke checks with red/green verification tests.

## API

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

## Intermediate checks and measurement applicability

Between formal calibrations a device must hold an intermediate check that is
currently in force before a new measurement can be recorded.

- A check is valid only when `result` is `passed`, `valid_until` is strictly
  after `performed_at`, and the validity window has not expired.
- Invalid check registrations are rejected inside the atomic commit, leaving no
  check, device pointer, or audit trace behind.
- A device has at most one current check. A failed check clears it; an
  already-expired pass is kept on the ledger but is never adopted.
- A measurement can only be created for a device with a passed, unexpired
  check, and that check id is pinned on the measurement at record time. Later
  checks never rewrite recorded or sealed measurements.
- The evidence seal freezes the check that the selected accepted measurement
  was performed under (`measurement_check_id`).

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
