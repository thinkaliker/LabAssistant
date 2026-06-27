# LabAssistant

A lightweight dashboard + docker orchestrator + host updater + more. AI assisted, human designed and reviewed.

## Philosophy

Ansible too complicated? PyInfra also too complicated? Portainer too bloated? This is intended to be a lightweight replacement for some of those, namely a single pane of glass dashboard with the ability to orchestrate docker compose files, restart containers, update images, and edit those compose files directly on the host. While we're at it, also notify if there are host package updates and apply those to the hosts.

## Required Prerequisites

- go
- tailscale (if using tailscale, otherwise you will need to route/open ports between your target hosts and your manager.)

## Components

### associate

a small agent which gets installed on each host. this agent communicates to the manager and also gets basic health and status of the host, as well as is the sole entrypoint to each host for LabAssistant. maintains a persistent mTLS stream to the manager which includes live statuses and a heartbeat (gRPC messages with a websocket fallback). can stream logs from modules to manager when requested. manages a command queue to serialize commands from the manager. actions should also report their progress so that long actions can have their state tracked by the manager and doesn't try to double apply if a similar task is queued. not privileged, but can kick off a helper that's elevated to run elevated actions when requested from modules.

### manager

the mastermind of the whole operation. manager talks to and listens to the associate agents. the internal state of each host and module states are kept in the manager and is displayed by the dashboard. observes liveliness from the mTLS stream connection. hosts and details about each host (including which modules are enabled), as well as any system settings are kept in a simple json file. assign and revoke certs when hosts are added/removed. certs are not kept in the json file. uses the quartermaster to orchestrate associate installs as well as module loading. uses the auditor to read/write audit logs. uses the scheduler to determine when to periodically run actions on hosts. manager exposes an API which the dashboard can then ingest but also other services - requires the same auth as login or an auth token. the manager API is specified in [API.md](API.md). future work: webhooks for external notification, home assistant integration.

#### quartermaster

this is an internal package inside of the manager. the quartermaster's role is negotiate the ssh connection to the hosts to initiate the associate installation, mTLS creation and exchange, and any protocol-version negotiation. it can also upgrade associates if enough has changed, to add new modules to each host, or to re-exchange mTLS certs. notify manager if certs are close to expiry. only interfaced with manager.

#### auditor

this is an internal package inside of the manager. append to and provide an audit log for changes performed on each host with details containing exactly what was updated or what was changed, logins, adding/removing hosts, certificate rotations, module add/remove/enable/disable, and any approvals for multi step actions. does not include sensitive information. uses a choice of log file, local sqlite, or other external database. hash chain entries and if an sqlite file is used, make a new user that is the only one allowed to read/write to the file. configurable retention/log length (eg. keep the last 1000 log entries). only interfaced with manager.

#### scheduler

this is an internal package inside of the manager. this manages any of the cron jobs surfaced by the dashboard and trigger the manager to perform those actions on specified hosts. user must specify a skip, catch-up, or retry per job if the host is offline, the manager was down at the time of attempting to run task, or if a task fails. a single task can be scheduled for multiple hosts and a time can be added to delay between hosts. scheduled tasks will require a confirmation when saving the task if a destructive action is taken (eg. reboot) - any edits to that task will re-ask for confirmation. tasks and their run state are persisted in the json file in manager. there should be a flag to indicate if the task is intended to be run based on the manager's time zone or the host's timezone.

### dashboard

a slim and lightweight web dashboard to view health and status of hosts/docker containers, kick off actions manually, add/remove hosts, manage scheduled tasks, and edit config files (specifically docker compose files on hosts) which serves as a frontend for manager via its API. validates compose files before writing to host and keep a local copy for undos. every status from the associates can result in an approval action or an automatic one (eg. run qup once a week automatically without asking me, or automatically rotate certs prior to expiry). can also stream logs from containers or the host system, or display the audit log (non editable). all settings (hosts, certs, settings, cron jobs, etc.) can be backed up from the dashboard and reimported on a fresh install. also includes a robust login page using credentials created during install.

## Modules

Modules are abilities that each associate is able to perform on each host. Modules provide actions for the associate to run. Each action will have configurable (with reasonable default) timeouts. The module contract that every module implements is specified in [API.md](API.md).

### duo

**d**ocker **u**pdater/**o**rchestrator - runs on the docker host. checks for new images (similar to watchtowerr), notifies the associate for updates, and also start/stop/restarts the docker compose stack. automatically detect container orchestration stack and uses the appropriate commands. stream docker logs to associate if requested. currently targeting only docker compose but can add others in the future (eg. swarm, podman). actions should be elevated if required by current user.

### qup

**q**uick **up**dater - inspired by the [rice.sh](https://github.com/thinkaliker/rice.sh) qup script, runs on the host which dry runs package manager updates, notifies the associate of updates, and performs those updates. automatically detects distro and runs the appropriate package manager commands. currently targeting debian based distros but can add more distros in the future. needs to run actions as privileged.

### sys

**sys**tem - runs host level system commands like streaming system logs to associate, host reboots (require confirmation), network interface listing, viewing disk usage, viewing uptime, or restarting specific system services (more commands can be added later but must be specifically defined). specific actions (reboot, some logs, restarting system services) should be elevated, otherwise no need to elevate.

### more tbd

## Workflow and Setup

1) Install the manager on a host. This will be your control host and single point of access. It generates mTLS certificates for use as the root CA.
2) Open the web dashboard.
3) Add a new host. Specify SSH user. Be sure to specify if tailscale is enabled for that host.
4) manager will attempt to ssh to the host and prompt the user for a password if no keys are available. If a key is already exchanged for the host, or tailscale is enabled for this host, skip this step.
5) quartermaster installs the associate onto the host over SSH.
6) associate and quartermaster perform mTLS auth exchange over SSH.
7) Start associate as a systemd (or equivalent) service.
8) associate pings manager and establishes connection via mTLS.
9) associate sends qup/duo/sys/etc status to manager.
10) dashboard sends qup/duo/sys/etc commands to manager.
11) manager sends commands to appropriate associate.
12) associate runs corresponding module action.
13) Continue adding hosts.

## TODO

Known items not yet designed into the components above.

### Core build

- Implement the manager, one associate, and the `qup` module end-to-end against the [API.md](API.md) contract to validate the dial-home mTLS stream, the command/queue/idempotency path, and the action → job → approval → audit flow.
- Internal event bus in the manager so the audit log, the dashboard live feed, webhooks, and Home Assistant are all subscribers rather than special-cased code.
- Job model: manager-issued command IDs + acks so a dropped/reconnected stream never double-applies a long-running action.

### Associate / modules

- Avoid command-queue head-of-line blocking: let read-only actions run alongside the single serialized mutating action.
- Per-module, per-host config + secrets storage (secrets kept out of the json file, like certs).
- Multi-arch / multi-OS associate installer (match host architecture; support service managers beyond systemd).
- Expand coverage over time: qup → more distros; duo → swarm/podman.
- Modules are compiled into the associate for v1; design toward external-binary modules later (the contract is already shaped for it).

### Scheduler

- Global concurrency cap + jitter for fleet-wide actions (beyond the per-host delay) to avoid stampedes.
- Catch-up grace window so a long-overdue job isn't run after extended downtime.
- Surface scheduled-run failures beyond the audit log (retry feedback + notification).

### Security

- SSH host-key verification (trust-on-first-use) during enrollment — it is the trust anchor for the mTLS bootstrap.
- Gate read access to the audit log (entries can reference sensitive operations).

### Future work

- Webhooks for external notification.
- Home Assistant integration.