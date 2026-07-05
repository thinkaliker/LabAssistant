# LabAssistant — Design

Full design notes for LabAssistant. The [README](README.md) carries the summary; this
document holds the detailed rationale and behaviour for each component and module. See
[API.md](API.md) for the module contract and manager API, and [BUILD.md](BUILD.md) for the
slice-by-slice build plan.

## Philosophy

Ansible too complicated? PyInfra also too complicated? Portainer too bloated? LabAssistant is
a lightweight replacement for some of those: a single pane of glass dashboard that can
orchestrate docker compose files, restart containers, update images, and edit those compose
files directly on the host. While we're at it, it also notifies when there are host package
updates and applies those to the hosts.

## Components

### associate

A small agent installed on each host. It communicates with the manager, gathers basic health
and status of the host, and is the sole entrypoint to each host for LabAssistant. It maintains
a single persistent mTLS stream with the manager that carries live statuses and a heartbeat
(gRPC messages with a websocket fallback). It can stream logs from modules to the manager when
requested.

The stream is bidirectional, so the same connection carries commands down (manager → associate)
and statuses, heartbeats, job events, and log chunks back up (associate → manager) regardless of
which side opened it. Which side dials is a per-host setting — see [connection modes](#connection-modes).

It manages a command queue to serialize commands from the manager. Actions report their
progress so that long-running actions can have their state tracked by the manager, which avoids
double-applying when a similar task is already queued. The associate is not privileged, but it
can kick off an elevated helper to run privileged actions when a module requests it.

### manager

The mastermind of the whole operation. The manager talks to and listens to the associate
agents — accepting streams from associates that dial home, and dialing out to associates that
listen (see [connection modes](#connection-modes)). The internal state of each host and the
module states are kept in the manager and displayed by the dashboard. It observes liveness from
the mTLS stream connection regardless of which side opened it.

Hosts and their details (including which modules are enabled), as well as any system settings,
are kept in a simple JSON file. The manager assigns and revokes certs when hosts are
added/removed; certs are **not** kept in the JSON file. It uses the quartermaster to orchestrate
associate installs and module loading, the auditor to read/write audit logs, and the scheduler
to determine when to periodically run actions on hosts.

The manager exposes an API the dashboard ingests — and that other services can use too — gated
by the same auth as login or an auth token. The API is specified in [API.md](API.md). Future
work: webhooks for external notification and Home Assistant integration.

#### connection modes

Every host has a connection mode chosen at enroll time that decides **which side opens the TCP
connection**. It does not change what flows over it: in both modes a single bidirectional mTLS
stream carries commands down to the associate and statuses/heartbeats/job events/logs back up to
the manager. Only the initial dial direction — and therefore which firewall must permit the
inbound connection — differs.

- **dial-home** (default): the associate dials the manager. The manager listens on its gRPC port
  (`grpc_addr`); the host only needs outbound access to the manager. The associate holds a
  client certificate (CN = host id) and the manager a server certificate; the manager identifies
  the host from the verified client-cert CommonName. This is the right choice when the manager is
  reachable but the hosts are not.

- **manager-dial**: the manager dials the associate. The associate listens on a per-host port
  (default 8444) and the manager connects out to it, retrying with backoff and holding one
  outbound stream per host. The roles reverse at the TLS layer — the associate presents a server
  certificate (CN = host id) and the manager a client certificate, and the manager pins the
  associate's identity by verifying that CommonName. This is the right choice when the hosts are
  reachable but the manager cannot accept inbound connections. The host firewall must allow the
  manager to reach the listen port; LabAssistant does not modify host firewalls.

Because the two directions share the same message protocol, everything downstream — command
queueing, job tracking, log streaming, liveness — is identical either way. Certificate rotation
currently applies to dial-home hosts only; a manager-dial host rotates by re-enrolling.

#### quartermaster

An internal package inside the manager. The quartermaster negotiates the SSH connection to the
hosts to initiate associate installation, mTLS creation and exchange, and any protocol-version
negotiation. The certificate it issues depends on the host's [connection mode](#connection-modes)
— a client certificate for dial-home, a server certificate for manager-dial. It can also upgrade
associates when enough has changed, add new modules to each host, or re-exchange mTLS certs. It notifies the manager when certs are close to expiry. Only
the manager interfaces with it.

#### auditor

An internal package inside the manager. The auditor appends to and provides an audit log for
changes performed on each host, with details of exactly what was updated or changed: logins,
adding/removing hosts, certificate rotations, module add/remove/enable/disable, and any
approvals for multi-step actions. It does not include sensitive information.

It uses a choice of log file, local SQLite, or other external database. Entries are hash-chained,
and if a SQLite file is used, a new user is created as the only one allowed to read/write the
file. Retention/log length is configurable (e.g. keep the last 1000 entries). Only the manager
interfaces with it.

#### scheduler

An internal package inside the manager. The scheduler manages the cron jobs surfaced by the
dashboard and triggers the manager to perform those actions on specified hosts. The user must
specify a skip, catch-up, or retry policy per job for when the host is offline, the manager was
down at the scheduled time, or a task fails.

A single task can be scheduled for multiple hosts, with an optional delay between hosts.
Scheduled tasks require a confirmation when saving if a destructive action is taken (e.g.
reboot); any edits to that task re-ask for confirmation. Tasks and their run state are persisted
in the manager's JSON file. A flag indicates whether the task runs based on the manager's
timezone or the host's timezone.

### dashboard

A slim, lightweight web dashboard to view health and status of hosts/docker containers, kick off
actions manually, add/remove hosts, manage scheduled tasks, and edit config files (specifically
docker compose files on hosts). It is a frontend for the manager via its API. It validates
compose files before writing to the host and keeps a local copy for undos.

Every status from the associates can result in an approval action or an automatic one (e.g. run
qup once a week automatically without asking, or automatically rotate certs prior to expiry). It
can also stream logs from containers or the host system, or display the audit log
(non-editable). All settings (hosts, certs, settings, cron jobs, etc.) can be backed up from the
dashboard and reimported on a fresh install. It includes a robust login page using credentials
created during install.

## Modules

Modules are abilities that each associate can perform on a host. Modules provide actions for the
associate to run. Each action has a configurable (with reasonable default) timeout. The module
contract that every module implements is specified in [API.md](API.md). For v1, modules are
compiled into the associate; the contract is shaped to allow external-binary modules later.

### duo — docker updater/orchestrator

Runs on the docker host. Checks for new images (similar to Watchtower), notifies the associate
of updates, and starts/stops/restarts the docker compose stack. It automatically detects the
container orchestration stack and uses the appropriate commands. It can stream docker logs to
the associate on request. Currently targets docker compose only; others (swarm, podman) can be
added later. Actions are elevated if required by the current user.

### qup — quick updater

Inspired by the [rice.sh](https://github.com/thinkaliker/rice.sh) qup script. Runs on the host,
dry-runs package manager updates, notifies the associate of available updates, and performs
those updates. It automatically detects the distro and runs the appropriate package manager
commands. Currently targets Debian-based distros; more can be added later. It needs to run
actions as privileged.

### sys — system

Runs host-level system commands: streaming system logs to the associate, host reboots (require
confirmation), network interface listing, viewing disk usage, viewing uptime, or restarting
specific system services. More commands can be added later but must be specifically defined.
Specific actions (reboot, some logs, restarting system services) are elevated; otherwise no
elevation is needed.

### more tbd

## Workflow and Setup

1. Install the manager on a host. This is your control host and single point of access. It
   generates mTLS certificates for use as the root CA.
2. Open the web dashboard.
3. Add a new host. Specify the SSH user, whether tailscale is enabled, and the
   [connection mode](#connection-modes) (dial-home by default, or manager-dial if the manager
   cannot accept inbound connections).
4. The manager attempts to SSH to the host and prompts for a password if no keys are available.
   If a key is already exchanged for the host, or tailscale is enabled, skip this step.
5. The quartermaster installs the associate onto the host over SSH, issuing a client certificate
   for a dial-home host or a server certificate (plus a listen port) for a manager-dial host.
6. The associate and quartermaster perform the mTLS auth exchange over SSH.
7. Start the associate as a systemd (or equivalent) service.
8. The mTLS stream is established: a dial-home associate dials the manager, while a manager-dial
   associate listens and the manager dials it (retrying with backoff until it is up).
9. The associate sends qup/duo/sys/etc. status to the manager over that stream.
10. The dashboard sends qup/duo/sys/etc. commands to the manager.
11. The manager sends commands to the appropriate associate.
12. The associate runs the corresponding module action.
13. Continue adding hosts.
