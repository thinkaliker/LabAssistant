#!/usr/bin/env bash
#
# manage.sh — build and lifecycle management for the LabAssistant manager.
#
# Wraps the Go build for all three binaries and the systemd unit that runs the
# manager (see labassistant-manager.service in this directory). Safe to run from
# anywhere; it locates the checkout relative to itself.
#
# Usage: ./manage.sh <command> [args]   (run ./manage.sh --help for the list)

set -euo pipefail

SERVICE_NAME="labassistant-manager"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKOUT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$SCRIPT_DIR/${SERVICE_NAME}.service"
LA_HOME="${LABASSISTANT_HOME:-$HOME/.labassistant}"

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

SUDO=""
[[ "$(id -u)" -ne 0 ]] && SUDO="sudo"

service_installed() { [[ -f "$UNIT_PATH" ]]; }
require_service()   { service_installed || die "$SERVICE_NAME is not installed. Run: $0 install-service"; }

build() {
  local what="${1:-all}"
  cd "$CHECKOUT"
  mkdir -p bin
  case "$what" in
    manager)   log "Building manager...";        go build -o bin/manager ./cmd/manager ;;
    associate) log "Building associate...";       go build -o bin/associate ./cmd/associate ;;
    helper)    log "Building associatehelper..."; go build -o bin/associatehelper ./cmd/associatehelper ;;
    all)
      log "Building manager, associate, associatehelper..."
      go build -o bin/manager ./cmd/manager
      go build -o bin/associate ./cmd/associate
      go build -o bin/associatehelper ./cmd/associatehelper
      ;;
    *) die "unknown build target: $what (want: manager|associate|helper|all)" ;;
  esac
  log "Build complete."
}

install_service() {
  [[ -f "$TEMPLATE" ]] || die "service template not found: $TEMPLATE"
  local user group tmp
  user="$(id -un)"; group="$(id -gn)"
  log "Rendering $SERVICE_NAME unit (User=$user, home=$LA_HOME, checkout=$CHECKOUT)..."
  tmp="$(mktemp)"
  sed -e "s|@USER@|$user|g" \
      -e "s|@GROUP@|$group|g" \
      -e "s|@CHECKOUT@|$CHECKOUT|g" \
      -e "s|@LA_HOME@|$LA_HOME|g" \
      "$TEMPLATE" > "$tmp"
  $SUDO install -m 0644 "$tmp" "$UNIT_PATH"
  rm -f "$tmp"
  $SUDO systemctl daemon-reload
  log "Installed $UNIT_PATH"
  log "Enable on boot + start with: $0 enable && $0 start"
}

uninstall_service() {
  service_installed || { warn "no unit at $UNIT_PATH — nothing to remove"; return 0; }
  $SUDO systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
  $SUDO rm -f "$UNIT_PATH"
  $SUDO systemctl daemon-reload
  log "Removed $SERVICE_NAME"
}

update() {
  cd "$CHECKOUT"
  log "Pulling latest on $(git rev-parse --abbrev-ref HEAD)..."
  git pull --ff-only
  build all
  if service_installed && $SUDO systemctl is-active --quiet "$SERVICE_NAME"; then
    log "Restarting service..."
    $SUDO systemctl restart "$SERVICE_NAME"
    log "Updated and restarted."
  else
    log "Built. Service not running; start it with: $0 start"
  fi
}

cmd_start()   { require_service; $SUDO systemctl start   "$SERVICE_NAME"; log "started"; }
cmd_stop()    { require_service; $SUDO systemctl stop    "$SERVICE_NAME"; log "stopped"; }
cmd_restart() { require_service; $SUDO systemctl restart "$SERVICE_NAME"; log "restarted"; }
cmd_enable()  { require_service; $SUDO systemctl enable  "$SERVICE_NAME"; log "enabled on boot"; }
cmd_disable() { require_service; $SUDO systemctl disable "$SERVICE_NAME"; log "disabled on boot"; }
cmd_status()  { require_service; $SUDO systemctl status  "$SERVICE_NAME" --no-pager; }
cmd_logs() {
  require_service
  case "${1:-}" in
    -f|--follow) $SUDO journalctl -u "$SERVICE_NAME" -f ;;
    "")          $SUDO journalctl -u "$SERVICE_NAME" --no-pager -n 100 ;;
    *)           $SUDO journalctl -u "$SERVICE_NAME" --no-pager -n "$1" ;;
  esac
}

usage() {
  cat <<USAGE
manage.sh — build and lifecycle for the LabAssistant manager

Usage: $0 <command> [args]

Build:
  build [manager|associate|helper|all]   build binaries into ./bin (default: all)
  update                                 git pull --ff-only, build all, restart if running

Service lifecycle (systemd unit: $SERVICE_NAME):
  install-service                        render + install the unit, daemon-reload
  uninstall-service                      stop, disable, and remove the unit
  enable | disable                       (un)set start-on-boot
  start | stop | restart                 control the running service
  status                                 systemctl status
  logs [-f | <N>]                        journal logs: follow, or last N lines (default 100)

Paths:
  checkout : $CHECKOUT
  home     : $LA_HOME
  unit     : $UNIT_PATH
USAGE
}

case "${1:-}" in
  build)             shift; build "${1:-all}" ;;
  update)            update ;;
  install-service)   install_service ;;
  uninstall-service) uninstall_service ;;
  enable)            cmd_enable ;;
  disable)           cmd_disable ;;
  start)             cmd_start ;;
  stop)              cmd_stop ;;
  restart)           cmd_restart ;;
  status)            cmd_status ;;
  logs)              shift; cmd_logs "${1:-}" ;;
  ""|-h|--help|help) usage ;;
  *) die "unknown command: $1 (try: $0 --help)" ;;
esac
