#!/usr/bin/env bash
#
# deploy.sh — bootstrap a LabAssistant manager on a fresh Debian-based dev VM.
#
# Clones (or updates) the repo, installs a recent enough Go toolchain if needed,
# builds the manager binary, and prints the next steps. Idempotent: safe to re-run.
#
# Usage:
#   ./deploy.sh [--dir <checkout>] [--branch <branch>] [--home <data-home>]
#
#   --dir     where to clone the repo        (default: ~/LabAssistant)
#   --branch  branch to check out            (default: main)
#   --home    manager config/data/logs home  (default: ~/.labassistant)
#
# It does NOT build the associate or start the manager — it gets you to the point
# where `manager setpass` + `manager serve` work.

set -euo pipefail

REPO_URL="https://github.com/thinkaliker/LabAssistant.git"
GO_MIN="1.26"            # must match the `go` directive in go.mod
GO_VERSION="1.26.0"      # version to install if Go is missing/too old
CHECKOUT="${HOME}/LabAssistant"
BRANCH="main"
LA_HOME="${HOME}/.labassistant"

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)    CHECKOUT="$2"; shift 2 ;;
    --branch) BRANCH="$2";   shift 2 ;;
    --home)   LA_HOME="$2";  shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
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
  PROFILE="${HOME}/.profile"
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
mkdir -p "$LA_HOME"
log "Manager home: $LA_HOME"

# --- 6. next steps -----------------------------------------------------------
cat <<EOF

$(log "Done.")

  Manager binary : $CHECKOUT/bin/manager
  Manager home   : $LA_HOME

Next steps:

  export LABASSISTANT_HOME="$LA_HOME"
  cd "$CHECKOUT"

  # 1. set the dashboard login password
  ./bin/manager setpass

  # 2. run the manager (dashboard on :8080, associate mTLS on :8443)
  ./bin/manager serve

Open the dashboard at http://<this-vm>:8080
EOF
