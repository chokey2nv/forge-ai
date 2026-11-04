#!/usr/bin/env bash
# Starts and stops a whole local Forge: adapter, control plane and console.
#
#   scripts/forge.sh up        start what isn't running, then open the console
#   scripts/forge.sh down      stop what this script started
#   scripts/forge.sh restart   down, then up (picks up code changes)
#   scripts/forge.sh status
#   scripts/forge.sh logs [control-plane|adapter|console]
#   scripts/forge.sh user <name> [--role viewer|member|reviewer] [--admin] [--email <address>]
#   scripts/forge.sh users
#   scripts/forge.sh eval [case ...]   score the agents on tests/evaluations; spends real money with a paid model
#
# Everything else comes from .env. The ports are set here and win over .env, so
# Forge stays off 8080/5173 on a machine where something else uses them:
# FORGE_CP_PORT, FORGE_ADAPTER_PORT and FORGE_CONSOLE_PORT override them.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN="${FORGE_RUN_DIR:-$ROOT/.forge/run}"
CP_PORT="${FORGE_CP_PORT:-8085}"
ADAPTER_PORT_="${FORGE_ADAPTER_PORT:-8086}"
CONSOLE_PORT="${FORGE_CONSOLE_PORT:-5174}"
CONSOLE_URL="http://localhost:$CONSOLE_PORT"
SERVICES="adapter control-plane console"
STARTED=""

# A window opened from Finder may not have the user's full PATH.
export PATH="$PATH:/opt/homebrew/bin:/usr/local/bin:$HOME/go/bin"

say() { printf '%s\n' "$*"; }
die() { printf 'Forge did not start: %s\n' "$*" >&2; exit 1; }

env_value() { # the value of a variable: from the environment, else from .env
  eval "local v=\${$1:-}"
  if [ -z "$v" ] && [ -f "$ROOT/.env" ]; then
    v="$(grep -E "^$1=" "$ROOT/.env" | tail -1 | cut -d= -f2- | sed -E 's/[[:space:]]+$//')"
  fi
  printf '%s' "$v"
}

port_of() {
  case "$1" in
    adapter) echo "$ADAPTER_PORT_" ;;
    control-plane) echo "$CP_PORT" ;;
    console) echo "$CONSOLE_PORT" ;;
  esac
}

# Part of the service's command line, so a recycled PID is never mistaken for it.
marker_of() {
  case "$1" in
    adapter) echo "src/server.ts" ;;
    control-plane) echo "$RUN/forge" ;;
    console) echo "vite" ;;
  esac
}

is_running() {
  local pidfile="$RUN/$1.pid" pid
  [ -f "$pidfile" ] || return 1
  pid="$(cat "$pidfile")"
  kill -0 "$pid" 2>/dev/null || return 1
  ps -p "$pid" -o command= | grep -qF "$(marker_of "$1")"
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is not installed or not on PATH ($2)"
}

check_prerequisites() {
  need docker "workers run in containers"
  need go "the control plane is built from source"
  need pnpm "the adapter and console use it"
  [ -f "$ROOT/.env" ] || die "there is no .env; copy .env.example to .env and fill it in"
  [ -n "$(env_value FORGE_ADAPTER_TOKEN)" ] ||
    die "FORGE_ADAPTER_TOKEN is empty in .env; set it to the output of: openssl rand -hex 32"
}

ensure_docker() {
  docker info >/dev/null 2>&1 && return 0
  say "Starting Docker..."
  open -a Docker 2>/dev/null || die "Docker is not running; start it and try again"
  local i
  for i in $(seq 1 60); do
    docker info >/dev/null 2>&1 && return 0
    sleep 2
  done
  die "Docker did not come up within 2 minutes"
}

ensure_mongo() {
  local uri port
  uri="$(env_value MONGODB_URI)"
  uri="${uri:-mongodb://127.0.0.1:27017}"
  # Only a local database can be checked and started from here.
  case "$uri" in
    mongodb://localhost*|mongodb://127.0.0.1*) ;;
    *) return 0 ;;
  esac
  port="$(printf '%s' "$uri" | sed -nE 's#^mongodb://[^:/]+:([0-9]+).*#\1#p')"
  port="${port:-27017}"
  nc -z 127.0.0.1 "$port" 2>/dev/null && return 0
  say "Starting MongoDB..."
  docker compose -f "$ROOT/infrastructure/compose/mongo.yaml" up -d >/dev/null ||
    die "could not start MongoDB (infrastructure/compose/mongo.yaml)"
  local i
  for i in $(seq 1 30); do
    nc -z 127.0.0.1 "$port" 2>/dev/null && return 0
    sleep 1
  done
  die "MongoDB did not come up on port $port"
}

ensure_built() {
  local image
  image="$(env_value WORKER_IMAGE)"
  image="${image:-forge-worker:dev}"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    say "Building the worker image $image (first time only)..."
    (cd "$ROOT" && make worker-build) || die "the worker image did not build"
  fi
  [ -x "$ROOT/services/runtime-adapter/node_modules/.bin/tsx" ] ||
    (cd "$ROOT/services/runtime-adapter" && pnpm install) || die "the adapter's packages did not install"
  [ -x "$ROOT/apps/console/node_modules/.bin/vite" ] ||
    (cd "$ROOT/apps/console" && pnpm install) || die "the console's packages did not install"
  is_running control-plane && return 0
  say "Building the control plane..."
  (cd "$ROOT/services/control-plane" && go build -o "$RUN/forge" ./cmd/forge) ||
    die "the control plane did not build"
}

check_port_free() { # $1 service
  local port listener
  port="$(port_of "$1")"
  listener="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | awk 'NR==2{print $1" (pid "$2")"}')"
  [ -z "$listener" ] && return 0
  die "port $port, for the $1, is in use by $listener. Pick another, e.g. FORGE_${2}_PORT=$((port + 10)) scripts/forge.sh up"
}

start_service() { # $1 service, $2 directory, the rest its command
  local name="$1" dir="$2"
  shift 2
  (cd "$dir" && exec nohup "$@" >>"$RUN/$name.log" 2>&1 </dev/null) &
  echo $! >"$RUN/$name.pid"
  STARTED="$STARTED $name"
}

stop_service() {
  local name="$1" pidfile="$RUN/$1.pid" pid i
  if ! is_running "$name"; then
    rm -f "$pidfile"
    return 1
  fi
  pid="$(cat "$pidfile")"
  pkill -TERM -P "$pid" 2>/dev/null
  kill -TERM "$pid" 2>/dev/null
  for i in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.5
  done
  if kill -0 "$pid" 2>/dev/null; then
    pkill -KILL -P "$pid" 2>/dev/null
    kill -KILL "$pid" 2>/dev/null
  fi
  rm -f "$pidfile"
  return 0
}

wait_for() { # $1 service, $2 URL
  local i
  for i in $(seq 1 120); do
    curl -q -sf -o /dev/null --max-time 2 "$2" && return 0
    is_running "$1" || break
    sleep 0.5
  done
  printf '\nThe %s did not come up. The end of its log (%s):\n' "$1" "$RUN/$1.log" >&2
  tail -20 "$RUN/$1.log" >&2
  local name
  for name in $STARTED; do stop_service "$name" >/dev/null; done
  die "see the log above"
}

cmd_up() {
  mkdir -p "$RUN"
  check_prerequisites
  ensure_docker
  ensure_mongo
  is_running adapter || check_port_free adapter ADAPTER
  is_running control-plane || check_port_free control-plane CP
  is_running console || check_port_free console CONSOLE
  ensure_built

  # What the services use to find each other; dotenv never overrides these.
  export ADAPTER_PORT="$ADAPTER_PORT_"
  export ADAPTER_URL="http://localhost:$ADAPTER_PORT_"
  export CONTROL_PLANE_ADDR="127.0.0.1:$CP_PORT"
  export CONTROL_PLANE_SELF_URL="http://localhost:$CP_PORT"
  export CONTROL_PLANE_URL="$CONTROL_PLANE_SELF_URL"
  export FORGE_CONSOLE_ORIGINS="$CONSOLE_URL,http://127.0.0.1:$CONSOLE_PORT"
  export VITE_API_URL="$CONTROL_PLANE_SELF_URL"

  # The adapter first: the control plane may resume a delivery as it starts.
  if ! is_running adapter; then
    say "Starting the adapter on $ADAPTER_PORT_..."
    start_service adapter "$ROOT/services/runtime-adapter" node_modules/.bin/tsx src/server.ts
  fi
  wait_for adapter "http://localhost:$ADAPTER_PORT_/health"
  if ! is_running control-plane; then
    say "Starting the control plane on $CP_PORT..."
    start_service control-plane "$ROOT/services/control-plane" "$RUN/forge"
  fi
  wait_for control-plane "http://localhost:$CP_PORT/health"
  if ! is_running console; then
    say "Starting the console on $CONSOLE_PORT..."
    start_service console "$ROOT/apps/console" node_modules/.bin/vite --port "$CONSOLE_PORT" --strictPort
  fi
  wait_for console "$CONSOLE_URL"

  say ""
  say "Forge is running: $CONSOLE_URL"
  say "Stop it with: scripts/forge.sh down"
  [ -n "${FORGE_NO_OPEN:-}" ] || open "$CONSOLE_URL" 2>/dev/null
  return 0
}

cmd_down() {
  local name stopped=""
  # The reverse of starting: nothing calls a service that's already gone.
  for name in console control-plane adapter; do
    stop_service "$name" && stopped="$stopped $name"
  done
  if [ -n "$stopped" ]; then say "Stopped:$stopped"; else say "Forge is not running."; fi
}

cmd_status() {
  local name
  for name in $SERVICES; do
    if is_running "$name"; then
      say "$name: running on $(port_of "$name") (pid $(cat "$RUN/$name.pid"))"
    else
      say "$name: stopped"
    fi
  done
}

cmd_logs() {
  local name="${1:-control-plane}"
  [ -f "$RUN/$name.log" ] || { say "No log yet at $RUN/$name.log"; exit 1; }
  tail -n 100 -f "$RUN/$name.log"
}

admin_command() { # the control plane's `forge users ...`, without its startup noise
  set -o pipefail
  (cd "$ROOT/services/control-plane" && go run ./cmd/forge "$@" 2>&1) |
    grep -vE 'loaded env from|^Password \(first line of stdin\)|^exit status|^$'
}

cmd_user() { # asks for the password, so it stays out of the shell's history and the screen
  local name="${1:-}" password confirm
  [ -n "$name" ] || { say "usage: scripts/forge.sh user <name> [--role viewer|member|reviewer] [--admin] [--email <address>]"; exit 2; }
  shift
  need go "the user command is built from source"
  printf 'Password for %s (10+ characters): ' "$name"
  IFS= read -rs password; echo
  printf 'Again: '
  IFS= read -rs confirm; echo
  [ "$password" = "$confirm" ] || { say "The passwords don't match; nothing was changed."; exit 1; }
  printf '%s\n' "$password" | admin_command users add "$name" "$@" || exit 1
  say "Sign in at $CONSOLE_URL. Unless they're an instance admin, add them to a project on its Project page."
}

# An API token for the runner, made once for the first instance admin.
eval_token() {
  local file="$RUN/eval-token" admin token
  if [ -s "$file" ] && [ "$(curl -q -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$file")" "http://localhost:$CP_PORT/auth/me")" = 200 ]; then
    cat "$file"
    return 0
  fi
  admin="$(admin_command users list | awk -F'\t' '$4 == "admin=true" && $5 == "disabled=false" {print $2; exit}')"
  [ -n "$admin" ] || die "no instance admin to run evals as; add one with make user NAME=... ADMIN=1"
  token="$(admin_command tokens create "$admin" "evals" | grep '^fgt_')" || die "could not create a token for $admin"
  (umask 077 && printf '%s' "$token" >"$file")
  printf '%s' "$token"
}

cmd_eval() {
  is_running control-plane && is_running adapter || { say "Forge is not running; start it first (scripts/forge.sh up)."; exit 1; }
  (cd "$ROOT/services/control-plane" && go build -o "$RUN/eval" ./cmd/eval) || exit 1
  local token
  token="$(eval_token)" || exit 1
  # The model Forge's adapter is set up with, not .env's EVAL_ values; set them
  # in the environment to compare another.
  cd "$ROOT" && FORGE_TOKEN="$token" CONTROL_PLANE_URL="http://localhost:$CP_PORT" \
    MODEL_PROVIDER="$(env_value MODEL_PROVIDER)" MODEL_NAME="$(env_value MODEL_NAME)" \
    "$RUN/eval" "$@"
}

case "${1:-up}" in
  up) cmd_up ;;
  down) cmd_down ;;
  restart) cmd_down; cmd_up ;;
  status) cmd_status ;;
  logs) cmd_logs "${2:-}" ;;
  user) shift; cmd_user "$@" ;;
  users) admin_command users list ;;
  eval) shift; cmd_eval "$@" ;;
  *) say "usage: scripts/forge.sh up|down|restart|status|logs [control-plane|adapter|console]|user <name>|users"; exit 2 ;;
esac
