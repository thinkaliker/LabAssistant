#!/usr/bin/env bash
#
# deploy.sh — bootstrap a LabAssistant manager on a fresh Debian-based dev VM.
#
# Clones (or updates) the repo, installs a recent enough Go toolchain if needed,
# builds the manager binary, installs the systemd service, and prints next steps.
# Idempotent: safe to re-run.
#
# Usage:
#   ./deploy.sh [--dir <checkout>] [--branch <branch>] [--home <data-home>] [--no-service]
#
#   --dir         where to clone the repo        (default: ~/LabAssistant)
#   --branch      branch to check out            (default: main)
#   --home        manager config/data/logs home  (default: ~/.labassistant)
#   --no-service  skip installing the systemd unit (run the manager manually)
#
# It installs (and enables on boot) the labassistant-manager systemd unit but does
# NOT start it — run `manager setpass` first. It does not build the associate.

set -euo pipefail

REPO_URL="https://github.com/thinkaliker/LabAssistant.git"
GO_MIN="1.26"            # must match the `go` directive in go.mod
GO_VERSION="1.26.0"      # version to install if Go is missing/too old
CHECKOUT="${HOME}/LabAssistant"
BRANCH="main"
LA_HOME="${HOME}/.labassistant"
PROFILE="${HOME}/.profile"
INSTALL_SERVICE=1

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)        CHECKOUT="$2"; shift 2 ;;
    --branch)     BRANCH="$2";   shift 2 ;;
    --home)       LA_HOME="$2";  shift 2 ;;
    --no-service) INSTALL_SERVICE=0; shift ;;
    -h|--help) sed -n '2,21p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "$(uname -s)" == "Linux" ]] || die "this script targets Debian-based Linux VMs"

# --- version compare: returns 0 if $1 >= $2 (dotted versions) ----------------
ver_ge() { [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]; }

# --- 1. base packages --------------------------------------------------------
APT_GET=""
if command -v apt-get >/dev/null 2>&1; then
  APT_GET="apt-get"
  SUDO=""
  [[ "$(id -u)" -ne 0 ]] && SUDO="sudo"
  log "Installing base packages (git, curl, ca-certificates)..."
  $SUDO $APT_GET update -qq
  $SUDO $APT_GET install -y -qq git curl ca-certificates
else
  warn "apt-get not found — assuming git/curl are already present."
  command -v git  >/dev/null 2>&1 || die "git is required"
  command -v curl >/dev/null 2>&1 || die "curl is required"
fi

# --- 2. Go toolchain ---------------------------------------------------------
NEED_GO=1
if command -v go >/dev/null 2>&1; then
  HAVE="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if [[ -n "$HAVE" ]] && ver_ge "$HAVE" "$GO_MIN"; then
    log "Go $HAVE already installed (>= $GO_MIN)."
    NEED_GO=0
  else
    warn "Go $HAVE is older than $GO_MIN — installing $GO_VERSION."
  fi
fi

if [[ "$NEED_GO" -eq 1 ]]; then
  case "$(uname -m)" in
    x86_64|amd64) GOARCH="amd64" ;;
    aarch64|arm64) GOARCH="arm64" ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
  TARBALL="go${GO_VERSION}.linux-${GOARCH}.tar.gz"
  log "Downloading $TARBALL..."
  curl -fsSL "https://go.dev/dl/${TARBALL}" -o "/tmp/${TARBALL}"
  log "Installing Go to /usr/local/go..."
  ${SUDO:-} rm -rf /usr/local/go
  ${SUDO:-} tar -C /usr/local -xzf "/tmp/${TARBALL}"
  rm -f "/tmp/${TARBALL}"
  export PATH="/usr/local/go/bin:${PATH}"
  # Persist PATH for future shells.
  if ! grep -q '/usr/local/go/bin' "$PROFILE" 2>/dev/null; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> "$PROFILE"
    log "Added /usr/local/go/bin to $PROFILE"
  fi
fi
command -v go >/dev/null 2>&1 || export PATH="/usr/local/go/bin:${PATH}"

# --- 3. clone or update the repo ---------------------------------------------
if [[ -d "$CHECKOUT/.git" ]]; then
  log "Updating existing checkout at $CHECKOUT..."
  git -C "$CHECKOUT" fetch --quiet origin
  git -C "$CHECKOUT" checkout --quiet "$BRANCH"
  git -C "$CHECKOUT" pull --quiet --ff-only origin "$BRANCH"
else
  log "Cloning $REPO_URL into $CHECKOUT..."
  git clone --quiet --branch "$BRANCH" "$REPO_URL" "$CHECKOUT"
fi

# --- 4. build the manager ----------------------------------------------------
# pb.go and the dashboard assets are committed, so no buf/codegen is needed here.
log "Building manager..."
cd "$CHECKOUT"
mkdir -p bin
go build -o bin/manager ./cmd/manager
log "Built $CHECKOUT/bin/manager"

# --- 5. prepare the data home ------------------------------------------------
mkdir -p "$LA_HOME/config"
log "Manager home: $LA_HOME"

# Persist LABASSISTANT_HOME for future shells so the manager finds this home.
if ! grep -q 'LABASSISTANT_HOME' "$PROFILE" 2>/dev/null; then
  echo "export LABASSISTANT_HOME=\"$LA_HOME\"" >> "$PROFILE"
  log "Added LABASSISTANT_HOME to $PROFILE"
fi

# Install the sample config on first deploy; never clobber an existing one.
CONFIG_DST="$LA_HOME/config/config.toml"
CONFIG_SRC="$CHECKOUT/config.sample.toml"
if [[ -f "$CONFIG_DST" ]]; then
  log "Config already present at $CONFIG_DST — leaving it as-is"
elif [[ -f "$CONFIG_SRC" ]]; then
  cp "$CONFIG_SRC" "$CONFIG_DST"
  log "Installed sample config to $CONFIG_DST (edit http_addr to change the dashboard port)"
else
  warn "Sample config $CONFIG_SRC not found — skipping config install"
fi

# --- 6. install the systemd service ------------------------------------------
# Installs and enables the unit on boot, but does NOT start it — the operator
# must `manager setpass` first. Lifecycle thereafter is via scripts/manage.sh.
SERVICE_READY=0
if [[ "$INSTALL_SERVICE" -eq 1 ]]; then
  if command -v systemctl >/dev/null 2>&1; then
    log "Installing + enabling systemd service (labassistant-manager)..."
    LABASSISTANT_HOME="$LA_HOME" "$CHECKOUT/scripts/manage.sh" install-service
    LABASSISTANT_HOME="$LA_HOME" "$CHECKOUT/scripts/manage.sh" enable
    SERVICE_READY=1
    log "Service enabled on boot (not started yet — run setpass first)."
  else
    warn "systemctl not found — skipping service install; run the manager manually."
  fi
else
  log "Skipping systemd service install (--no-service)."
fi

# --- 7. next steps -----------------------------------------------------------
cat <<EOF

$(log "Done.")

  Manager binary : $CHECKOUT/bin/manager
  Manager home   : $LA_HOME
  Config file    : $CONFIG_DST

LABASSISTANT_HOME was added to $PROFILE for future shells. For this one:

  export LABASSISTANT_HOME="$LA_HOME"
  cd "$CHECKOUT"

  # 1. set the dashboard login password
  ./bin/manager setpass
EOF

if [[ "$SERVICE_READY" -eq 1 ]]; then
cat <<EOF

  # 2. start the manager service (enabled on boot already)
  ./scripts/manage.sh start
  ./scripts/manage.sh status      # check it
  ./scripts/manage.sh logs -f     # follow logs

Manage the lifecycle with ./scripts/manage.sh (start|stop|restart|status|logs|update).
EOF
else
cat <<EOF

  # 2. run the manager (dashboard on :8080, associate mTLS on :8443)
  ./bin/manager serve
EOF
fi

cat <<EOF

To change the dashboard port, edit http_addr in $CONFIG_DST before serving.
Open the dashboard at http://<this-vm>:8080  (or whichever http_addr you set)
EOF
