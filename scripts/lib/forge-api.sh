# Sourced by the slice scripts: sign their API calls (Phase 13.2) and give
# them a project to work in (13.4). Needs CP set before the first call.
#
#   FORGE_TOKEN    an API token: forge tokens create <user> <name>
#   FORGE_PROJECT  the project's ID; unset, the project named "slices"
#                  (created if the token's user is an instance admin)

: "${FORGE_TOKEN:?export FORGE_TOKEN (an API token: forge tokens create <user> <name>)}"
# Every curl sends the token, through a private config file.
export CURL_HOME=$(mktemp -d "${TMPDIR:-/tmp}/forge-curl.XXXXXX")
printf 'header = "Authorization: Bearer %s"\n' "$FORGE_TOKEN" >"$CURL_HOME/.curlrc"
PROJECT=${FORGE_PROJECT:-}

# forge_register_repo PATH: makes PATH one of the project's repositories.
forge_register_repo() {
  if [ -z "$PROJECT" ]; then
    PROJECT=$(curl -sf "$CP/projects" | jq -r '[.[] | select(.name == "slices")][0].id // empty')
    [ -n "$PROJECT" ] || PROJECT=$(curl -sf -X POST "$CP/projects" -H 'Content-Type: application/json' -d '{"name":"slices"}' | jq -r '.id // empty')
    [ -n "$PROJECT" ] || { echo "no project: set FORGE_PROJECT, or use an instance admin's token" >&2; exit 1; }
    echo "project: $PROJECT"
  fi
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$CP/projects/$PROJECT/repos" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg p "$1" '{path: $p}')")
  case "$code" in
    201|409) ;; # 409: already registered
    *) echo "could not register $1 in project $PROJECT (HTTP $code): you need the project's admin role" >&2; exit 1 ;;
  esac
}
