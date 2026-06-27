# LabAssistant — Build Plan

How we build LabAssistant: thin vertical slices that prove the riskiest seams end-to-end
(mTLS dial-home stream, the command/queue path, a real module) before adding breadth.

See [README.md](README.md) for the architecture and [API.md](API.md) for the module contract
and manager API.

## Prerequisites

- **Go 1.24+**
- **buf** (proto codegen)

## Phase 0 — Scaffolding & codegen

- `buf generate` → `proto/v1/*.pb.go`; `go mod tidy` to pull grpc + protobuf.
- Repo skeleton:
  ```
  cmd/{manager,associate,associatehelper}/main.go
  manager/{quartermaster,auditor,scheduler,api}/
  associate/
  module/                     # the Go contract (interface + types from API.md Part 1)
  modules/{duo,qup,sys}/
  dashboard/                  # static assets (Alpine + Bulma, vendored), go:embed
  proto/v1/
  ```
  Move the existing top-level `quartermaster/` under `manager/`.
- **`module` package** — the spine everything imports: `Module` interface plus `Manifest`,
  `ActionSpec`, `Detection`, `Status`, `ActionRequest`, `Event`, `Result`, and enums, matching
  API.md Part 1 (including `Manifest.ConfigSchema`).
- Config loader + on-disk dirs (`config/ data/ logs/`), base overridable via `--home` /
  `LABASSISTANT_HOME`. See README / API.md for layout.
- Vendor pinned **Alpine.js** + **Bulma** into `dashboard/vendor/` (no CDN); embed via `go:embed`.

## First slice — walking skeleton

**Goal:** enroll one host, the associate dials home over mTLS, advertises `qup`, and you run a
qup action from a minimal dashboard and watch it stream to completion.

### In scope

- **manager**
  - Generate root CA + server cert; gRPC server for `ManagerService.Connect` with mTLS
    (verify client cert against the CA).
  - Host registry persisted to `state.json`; liveness derived from the stream connection.
  - REST subset: `GET /overview`, `GET /hosts`, `GET /hosts/{id}`, `GET /hosts/{id}/status`,
    `GET /hosts/{id}/modules`, `POST /hosts/{id}/modules/{name}/actions/{action}`,
    `GET /jobs/{id}`, `GET /jobs/{id}/events` (SSE), `GET /events` (SSE).
  - Serve the embedded dashboard. Minimal single-user login.
- **associate**
  - gRPC client that dials home and keeps the stream open; sends `Hello` (manifests + detection),
    runs a heartbeat loop.
  - Per-host command queue: serialize, and dedupe by `job_id` (idempotency).
  - Route `Command → module.Execute`; stream `JobEvent` / `JobResult` back up.
- **modules/qup**
  - `Detect` (apt / Debian-based), `Status` (apt dry-run: available updates), `Execute("apply")`.
- **dashboard**
  - Alpine + Bulma shell: navbar, Overview cards, Hosts list (thin rows) → expand → modules →
    "run qup" button. Live updates via `/events` + per-job SSE.

### Deliberate shortcuts (built properly in Slice 2 — agreed deferments)

1. **Enrollment:** skip SSH / quartermaster. A `manager` dev subcommand mints an associate bundle
   (host_id + client cert/key + manager address + CA) that is copied to the host and run manually.
   This proves the stream; the SSH-based Add Host flow lands in Slice 2.
2. **Privileged helper:** skip it — run the associate as root in dev. The real `associatehelper`
   privilege split lands in Slice 2.
3. **Auth:** include a minimal single-user login (it is the front door), but defer API tokens and
   session hardening.

### Acceptance criteria

- `GET /overview` shows 1 online host.
- Expanding the host shows `qup` with detected capabilities.
- "qup status" returns available updates.
- "qup apply" streams progress → `succeeded`; the job ends in state `succeeded`.
- Killing the associate flips the host to `offline` in the UI within seconds.

## Roadmap after Slice 1

- **Slice 2** — quartermaster SSH enrollment + async Add Host modal with progress; the privileged
  `associatehelper`.
- **Slice 3** — `duo` module + Services page (stack + service control) + log streaming.
- **Slice 4** — scheduler + tasks + the approval / destructive-action flow.
- **Slice 5** — auditor + audit log; `sys` module.
- **Slice 6** — settings, module config, backup/restore, cert rotation, token auth, associate
  self-update.

See the README **TODO** section for cross-cutting items (event bus, multi-arch installer, etc.)
that thread through these slices.
