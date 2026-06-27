# LabAssistant — Module Contract & Manager API

Reference for the two extension surfaces: the **module contract** (Go interface implemented by host
capabilities) and the **manager API** (REST/JSON consumed by clients).

## Architecture

| Boundary | Protocol |
|----------|----------|
| manager ↔ associate | persistent gRPC bidirectional stream over mTLS (associate-initiated) |
| manager ↔ clients (dashboard, CLI, Home Assistant, webhooks) | REST/JSON over TLS |
| associate ↔ modules | in-process Go interface (compiled-in) |

---

## Module Contract

A module is a Go package implementing `Module`. All metadata required by the manager and dashboard
is exposed through `Manifest()`; no core changes are needed to add a module.

### Interface

```go
type Module interface {
    Manifest() Manifest
    Detect(ctx context.Context) (Detection, error)
    Status(ctx context.Context) (Status, error)
    Execute(ctx context.Context, req ActionRequest, emit func(Event)) (Result, error)
}
```

| Method | Mutating | Description |
|--------|----------|-------------|
| `Manifest` | no | Static metadata and the list of supported actions. |
| `Detect` | no | Reports applicability to the host and detected capabilities. |
| `Status` | no | Read-only/dry-run snapshot of current state. |
| `Execute` | yes | Runs a named action; `emit` streams progress/log events. |

### Types

```go
type Manifest struct {
    Name         string
    Version      string
    Description  string
    Actions      []ActionSpec
    ConfigSchema json.RawMessage // JSON Schema for module-level per-host config (optional)
}

type ActionSpec struct {
    Name           string
    Description    string
    ParamsSchema   json.RawMessage // JSON Schema for request params
    ResultSchema   json.RawMessage // JSON Schema for the result payload
    Privilege      Privilege       // None | Elevated
    Destructive    bool
    DefaultTimeout time.Duration
    Streams        bool
}

type Privilege int // None, Elevated

type Detection struct {
    Applicable   bool
    Capabilities map[string]string // e.g. {"distro": "debian", "orchestrator": "compose"}
}

type Status struct {
    Summary string
    Data    json.RawMessage
}

type ActionRequest struct {
    JobID  string
    Action string
    Params json.RawMessage
}

type Event struct {
    JobID    string
    Kind     EventKind // Log | Progress | State
    Message  string
    Progress float64   // 0.0–1.0 when Kind == Progress
}

type Result struct {
    State JobState        // Succeeded | Failed | TimedOut
    Data  json.RawMessage
}
```

#### `ActionSpec` fields

| Field | Type | Notes |
|-------|------|-------|
| `Name` | string | Unique within the module. Used in the action endpoint path. |
| `ParamsSchema` | JSON Schema | Validated before dispatch; drives generic dashboard form rendering. |
| `ResultSchema` | JSON Schema | Shape of `Result.Data`. |
| `Privilege` | enum | `Elevated` actions run via the associate's privileged helper. |
| `Destructive` | bool | Requires confirmation to run manually; requires explicit opt-in to schedule. |
| `DefaultTimeout` | duration | Overridable per task. |
| `Streams` | bool | Whether the action emits `Event`s during execution. |

### Semantics

- Privilege is declared per action, not per module. Modules request elevation; the associate owns the
  single privileged helper.
- Each `Execute` carries a manager-issued `JobID`. The associate serializes actions per host and
  rejects an identical action already queued or running (idempotency).
- `Destructive` actions require confirmation when run manually and explicit opt-in to be scheduled.
- A module may declare `Manifest.ConfigSchema` for per-host configuration (e.g. a private-registry
  credential for `duo`). The manager stores this config per host and serves it via the module config
  endpoints; config secrets are kept outside `state.json`.
- Built-in modules: `duo`, `qup`, `sys`.
- External modules: `Manifest`/`ActionSpec` map to a JSON/stdio (or local gRPC) protocol; an
  out-of-process module loader can be added without changing the interface.

---

## Manager API

Base path: `/api/v1`. All requests and responses are JSON over TLS.

### Authentication

Every endpoint requires authentication via one of:

- Session cookie established by `POST /auth/login`.
- `Authorization: Bearer <token>` — tokens are minted and revoked via `/auth/tokens`.

### Conventions

- List endpoints paginate with `?limit=` and `?cursor=`; responses include `next_cursor`.
- Server→client streams use Server-Sent Events (`Accept: text/event-stream`).
- Timestamps are RFC 3339 UTC.

### Errors

Non-2xx responses use:

```json
{ "error": { "code": "string", "message": "string" } }
```

| Status | Meaning |
|--------|---------|
| 400 | Invalid request / schema validation failure |
| 401 | Missing or invalid authentication |
| 403 | Authenticated but not permitted |
| 404 | Resource not found |
| 409 | Conflict (e.g. duplicate in-flight action) |
| 422 | Action rejected by policy (e.g. unconfirmed destructive action) |

### Endpoints

| Method & path | Description |
|---------------|-------------|
| `POST /auth/login` | Establish a session. |
| `POST /auth/logout` | End the session. |
| `POST /auth/tokens` | Mint an API token. |
| `DELETE /auth/tokens/{id}` | Revoke an API token. |
| `GET /overview` | Lab-wide aggregate summary (see Overview). |
| `GET /hosts` | List hosts. |
| `POST /hosts` | Start async host enrollment (see Host lifecycle). |
| `GET /hosts/{id}` | Get host detail. |
| `PUT /hosts/{id}` | Update editable host fields (ip, tailscale, ssh_user, enabled modules). |
| `DELETE /hosts/{id}` | Remove a host (revokes its cert). |
| `GET /hosts/{id}/status` | Live health and per-module states. |
| `GET /hosts/{id}/modules` | Enabled modules with manifests and detection results. |
| `POST /hosts/{id}/modules/{name}:enable` | Enable a module on the host. |
| `POST /hosts/{id}/modules/{name}:disable` | Disable a module on the host. |
| `GET /hosts/{id}/modules/{name}/config` | Read a module's per-host config + schema. |
| `PUT /hosts/{id}/modules/{name}/config` | Update a module's per-host config (validated against schema). |
| `POST /hosts/{id}/modules/{name}/actions/{action}` | Start an action. Returns `{ "jobId": "..." }`. |
| `GET /services` | Aggregate of compose stacks + services across all hosts (see Services). |
| `GET /jobs` | List jobs. Filter with `?host=`, `?status=`. |
| `GET /jobs/{id}` | Get job detail and result. |
| `GET /jobs/{id}/events` | SSE stream of progress and log events. |
| `GET /approvals` | List pending approvals. |
| `POST /approvals/{id}:confirm` | Confirm a pending action. |
| `POST /approvals/{id}:reject` | Reject a pending action. |
| `GET /tasks` | List scheduled tasks. |
| `POST /tasks` | Create a scheduled task. |
| `PUT /tasks/{id}` | Update a scheduled task. |
| `DELETE /tasks/{id}` | Delete a scheduled task. |
| `GET /hosts/{id}/files?path=` | Read a config file. |
| `PUT /hosts/{id}/files` | Validate, back up, and write a config file. |
| `POST /hosts/{id}/files:undo` | Restore the last local copy. |
| `GET /hosts/{id}/logs` | SSE stream of host or container logs. |
| `GET /audit` | Read the audit log. |
| `GET /events` | SSE stream of host online/offline, job, and status updates. |
| `GET /settings` | Read manager settings. |
| `PUT /settings` | Update manager settings. |
| `GET /backup` | Export settings. |
| `POST /restore` | Import settings. |

### Action lifecycle

1. `POST` to an action endpoint creates a **job** in state `queued` and returns its `jobId`.
2. If the action's `Destructive` flag is set, or policy requires approval, the manager creates a
   pending **approval** and holds the job; otherwise it dispatches immediately.
3. `POST /approvals/{id}:confirm` dispatches the command over the associate's mTLS stream.
4. The associate executes the action; clients follow `GET /jobs/{id}/events` for progress and logs.
5. The manager records the `Result` and writes an audit entry.

### Job states

`queued` → `running` → (`succeeded` | `failed` | `timed_out`)

### Streaming

`GET /events` is the aggregate live feed; clients subscribe to it for host, job, and status changes
rather than polling. `GET /jobs/{id}/events` and `GET /hosts/{id}/logs` are scoped streams.

### Host lifecycle

A host's `state` is one of `enrolling | online | offline | error`.

Enrollment is asynchronous. `POST /hosts` accepts:

```json
{ "ip": "10.0.0.5", "tailscale": true, "ssh_user": "admin", "ssh_password": "…", "modules": ["duo", "qup", "sys"] }
```

`ssh_password` is optional and **transient** — used only for the SSH bootstrap, never persisted to
`state.json`. The call returns immediately with the host in `enrolling` state plus a `jobId`:

```json
{ "host": { "id": "h1", "state": "enrolling" }, "jobId": "…" }
```

Flow: `POST /hosts` → SSH → install associate → mTLS exchange → associate dials home → `online`.
Progress is reported on `GET /jobs/{id}/events` and the aggregate `GET /events`. Failures move the
host to `error` with detail in the job result.

### Overview

`GET /overview` returns a lab-wide aggregate for the dashboard's default page (shape may be refined):

```json
{
  "hosts":     { "total": 8, "online": 7, "offline": 1, "enrolling": 0 },
  "updates":   { "packages": 23, "images": 4 },
  "resources": { "cpu_percent": 31.2, "mem_percent": 44.0 },
  "services":  { "total": 40, "running": 38, "stopped": 2 }
}
```

### Services

`GET /services` is a **read-only projection** over the `duo` module across all hosts — compose stacks
with their nested services:

```json
{
  "stacks": [
    {
      "host_id": "h1", "name": "media", "path": "/srv/media/compose.yaml", "status": "running",
      "services": [
        { "name": "jellyfin", "status": "running", "image": "jellyfin/jellyfin:latest", "has_logs": true },
        { "name": "sonarr",   "status": "stopped", "has_logs": true }
      ]
    }
  ]
}
```

Control is **not** a separate surface — start/stop/restart route through `duo` actions on the owning
host:

```http
POST /hosts/{host_id}/modules/duo/actions/{start|stop|restart}
{ "stack": "media", "service": "jellyfin" }
```

Omit `service` for stack-level control; include it for a single service (both levels are supported).
Logs use `GET /hosts/{host_id}/logs` with service/container parameters. (v1 covers compose services
only; a user-defined non-docker service registry is deferred.)

### Module config

`GET /hosts/{id}/modules/{name}/config` returns the stored config and its schema (from
`Manifest.ConfigSchema`), so a client can render a generic settings form:

```json
{ "config": { "…": "…" }, "schema": { "…": "JSON Schema" } }
```

`PUT` validates the body against the schema before storing. Config secrets are persisted outside
`state.json`.

### Settings

`GET /settings` / `PUT /settings` manage manager-wide configuration (listen/TLS, scheduler default
timezone, audit retention, etc.). User credentials and API tokens are managed via the `/auth/*`
endpoints; the audit log and backup/restore have their own endpoints above.
