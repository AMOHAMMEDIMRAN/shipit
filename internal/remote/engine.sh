# shipit engine for POSIX servers (Linux, macOS). Embedded in the shipit program
# and sent over SSH for every command. Nothing is installed on the server.
#
# Everything arrives as SHIPIT_* variables. SHIPIT_MODE picks what to do.
#
# Layout under SHIPIT_APP_DIR:
#   releases/<release>/   one folder per version
#   current               link to the live release, switched in one step
#   shared/               the env file and preserved folders; survive every deploy
#   history.log           one line per deploy: time|release|version|result

# Non-interactive shells skip ~/.bashrc, so add the usual places for node, npm, pm2 and docker.
# They go AFTER the login's own PATH, so they only fill gaps and never shadow a real tool.
export PATH="$PATH:$HOME/.local/bin:$HOME/.bun/bin:/usr/local/bin"
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
if [ -s "$NVM_DIR/nvm.sh" ]; then . "$NVM_DIR/nvm.sh" >/dev/null 2>&1; fi

set -Eeuo pipefail

MODE="${SHIPIT_MODE:?SHIPIT_MODE is not set}"
APP_DIR="${SHIPIT_APP_DIR:-}"
RUNTIME="${SHIPIT_RUNTIME:-}"
ENGINE="${SHIPIT_ENGINE:-docker}"
NAME="${SHIPIT_NAME:-app}"
HEALTH_URL="${SHIPIT_HEALTH_URL:-}"
HEALTH_TRIES="${SHIPIT_HEALTH_TRIES:-30}"
HEALTH_SLEEP="${SHIPIT_HEALTH_SLEEP:-2}"
KEEP="${SHIPIT_KEEP:-3}"
ENV_LINK_AS="${SHIPIT_ENV_LINK_AS:-}"
ENV_REMOTE_PATH="${SHIPIT_ENV_REMOTE_PATH:-}"
ENV_MODE="${SHIPIT_ENV_MODE:-600}"
REQUIRED="${SHIPIT_REQUIRED:-}"
RULES="${SHIPIT_RULES:-}"
COMPOSE_FILE="${SHIPIT_COMPOSE_FILE:-}"
COMPOSE_PROJECT="${SHIPIT_COMPOSE_PROJECT:-}"
HAS_BUILD="${SHIPIT_HAS_BUILD:-0}"
PORT="${SHIPIT_PORT:-}"
SERVICE="${SHIPIT_SERVICE:-}"
PM2_FILE="${SHIPIT_PM2_FILE:-}"
BUILD_CMD="${SHIPIT_BUILD_CMD:-}"
START_CMD="${SHIPIT_START_CMD:-}"
STOP_CMD="${SHIPIT_STOP_CMD:-}"
STATUS_CMD="${SHIPIT_STATUS_CMD:-}"
PRE_SWITCH="${SHIPIT_PRE_SWITCH:-}"
POST_DEPLOY="${SHIPIT_POST_DEPLOY:-}"
PRESERVE="${SHIPIT_PRESERVE:-}"
DOCKERFILE="${SHIPIT_DOCKERFILE:-}"
BUILD_CONTEXT="${SHIPIT_BUILD_CONTEXT:-.}"
BUILD_TARGET="${SHIPIT_BUILD_TARGET:-}"
BUILD_PLATFORM="${SHIPIT_BUILD_PLATFORM:-}"
BUILD_ARGS="${SHIPIT_BUILD_ARGS:-}"
IMAGE="${SHIPIT_IMAGE:-}"
VERSION_TAG="${SHIPIT_VERSION_TAG:-}"
VERSION_LABEL="${SHIPIT_VERSION_LABEL:-}"

SUDO="sudo -n"
command -v sudo >/dev/null 2>&1 || SUDO=""

log()   { printf '%s\n' "$*"; }
fail()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
begin() { printf '@@begin %s\n' "$1"; }
finish(){ printf '@@done %s\n' "$1"; }

if [ -n "$APP_DIR" ]; then
  REL_DIR="$APP_DIR/releases"; CUR="$APP_DIR/current"; SHARED="$APP_DIR/shared"; HIST="$APP_DIR/history.log"
fi

ctr() { "$ENGINE" "$@" </dev/null; }

# dc runs compose in the current folder, adding shipit's override file if there is one.
dc() {
  local f=(-f "$COMPOSE_FILE")
  [ -f compose.shipit.yml ] && f+=(-f compose.shipit.yml)
  "$ENGINE" compose -p "$COMPOSE_PROJECT" "${f[@]}" "$@" </dev/null
}

ver_of() { local r="${1#*-}"; printf '%s' "${r#*-}"; }
releases() { ls -1 "$REL_DIR" 2>/dev/null | sort || true; }
live_rel() { if [ -L "$CUR" ]; then basename "$(readlink "$CUR")"; fi; }

point_current() {
  ln -sfn "$REL_DIR/$1" "$APP_DIR/.current.tmp" && mv -Tf "$APP_DIR/.current.tmp" "$CUR" 2>/dev/null \
    || { rm -f "$CUR"; ln -sfn "$REL_DIR/$1" "$CUR"; }
}

record() { printf '%s|%s|%s|%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$2" "$3" >> "$HIST"; }

# ---------------- runtimes ----------------

check_runtime() {
  for c in tar curl; do command -v "$c" >/dev/null 2>&1 || fail "The server is missing '$c'. Install it with the system package manager."; done
  case "$RUNTIME" in
    compose|container)
      command -v "$ENGINE" >/dev/null 2>&1 || fail "$ENGINE is not installed on the server."
      "$ENGINE" info >/dev/null 2>&1 || fail "$ENGINE is installed, but this user cannot use it. For docker, add the user to the docker group: sudo usermod -aG docker \$USER, then log in again."
      if [ "$RUNTIME" = compose ]; then
        "$ENGINE" compose version >/dev/null 2>&1 || fail "'$ENGINE compose' is not available on the server. Install the compose plugin."
      fi ;;
    pm2)
      command -v pm2 >/dev/null 2>&1 || fail "pm2 is not installed on the server. Install it with: npm install -g pm2"
      pm2 --version >/dev/null 2>&1 || fail "pm2 is on the server but does not run for this login. Check it by running 'pm2 --version' over SSH." ;;
    systemd)
      systemctl cat "$SERVICE" >/dev/null 2>&1 || fail "The systemd unit '$SERVICE' does not exist on the server."
      [ -z "$SUDO" ] || $SUDO true 2>/dev/null || fail "Restarting '$SERVICE' needs passwordless sudo for systemctl." ;;
  esac
}

build_release() {   # runs inside the new release folder
  case "$RUNTIME" in
    compose)
      if [ "$HAS_BUILD" = 1 ]; then log "Building images"; dc build; else log "Pulling images"; dc pull; fi ;;
    container)
      local img="$IMAGE:$VERSION_TAG" a
      local args=(build -t "$img" -f "$DOCKERFILE")
      [ -z "$BUILD_TARGET" ] || args+=(--target "$BUILD_TARGET")
      [ -z "$BUILD_PLATFORM" ] || args+=(--platform "$BUILD_PLATFORM")
      while IFS= read -r a; do [ -z "$a" ] || args+=(--build-arg "$a"); done <<< "$BUILD_ARGS"
      args+=("$BUILD_CONTEXT")
      log "Building image $img"
      ctr "${args[@]}"
      printf '%s\n' "$img" > .shipit-image ;;
    pm2|systemd|script)
      if [ -n "$BUILD_CMD" ]; then log "Running: $BUILD_CMD"; bash -c "$BUILD_CMD" </dev/null; fi ;;
  esac
}

activate() {
  case "$RUNTIME" in
    compose) ( cd "$CUR" && dc up -d --remove-orphans ) ;;
    container)
      local img envf=()
      img=$(cat "$CUR/.shipit-image")
      if [ -n "$ENV_LINK_AS" ] && [ -f "$ENV_REMOTE_PATH" ]; then envf=(--env-file "$ENV_REMOTE_PATH"); fi
      ctr rm -f "$NAME" >/dev/null 2>&1 || true
      ctr run -d --name "$NAME" --restart unless-stopped -p "$PORT" ${envf[@]+"${envf[@]}"} "$img" >/dev/null ;;
    pm2)
      ( cd "$CUR"
        if [ -n "$PM2_FILE" ]; then pm2 startOrReload "$CUR/$PM2_FILE" --update-env
        else pm2 delete "$NAME" >/dev/null 2>&1 || true; pm2 start "$START_CMD" --name "$NAME"; fi
        pm2 save >/dev/null ) ;;
    systemd) $SUDO systemctl restart "$SERVICE" ;;
    script) ( cd "$CUR" && bash -c "$START_CMD" </dev/null ) ;;
  esac
}

running() {
  case "$RUNTIME" in
    compose)
      ( cd "$CUR" && [ -n "$(dc ps -q 2>/dev/null)" ] && [ -z "$(dc ps -q --status exited --status restarting 2>/dev/null)" ] ) ;;
    container) [ "$(ctr inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = true ] ;;
    pm2) pm2 describe "$NAME" 2>/dev/null | grep -q online ;;
    systemd) systemctl is-active --quiet "$SERVICE" ;;
    script) [ -z "$STATUS_CMD" ] || ( cd "$CUR" && bash -c "$STATUS_CMD" </dev/null >/dev/null 2>&1 ) ;;
  esac
}

healthy() {
  local i
  for i in $(seq 1 "$HEALTH_TRIES"); do
    if [ -n "$HEALTH_URL" ]; then
      curl -fsS -o /dev/null --max-time 5 "$HEALTH_URL" 2>/dev/null && return 0
    else
      running && return 0
    fi
    sleep "$HEALTH_SLEEP"
  done
  return 1
}

show_logs() {
  case "$RUNTIME" in
    compose) ( cd "$CUR" && dc logs --tail "${1:-100}" ) ;;
    container) ctr logs --tail "${1:-100}" "$NAME" 2>&1 ;;
    pm2) pm2 logs "$NAME" --lines "${1:-100}" --nostream 2>&1 ;;
    systemd) $SUDO journalctl -u "$SERVICE" -n "${1:-100}" --no-pager ;;
    script) log "This project runs from a script, so shipit has no logs to show. Look where your start command writes them." ;;
  esac
}

cleanup_release() {   # remove the images that belong to a release about to be deleted
  case "$RUNTIME" in compose|container) ;; *) return 0 ;; esac
  local d="$1" i
  if [ -f "$d/.shipit-image" ]; then ctr rmi "$(cat "$d/.shipit-image")" >/dev/null 2>&1 || true; fi
  if [ -f "$d/compose.shipit.yml" ]; then
    grep -E '^[[:space:]]+image:' "$d/compose.shipit.yml" | awk '{print $2}' | while read -r i; do
      ctr rmi "$i" >/dev/null 2>&1 || true
    done
  fi
}

# ---------------- env file ----------------

envval() { grep -E "^$2=" "$1" 2>/dev/null | tail -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//" || true; }

# Prints  status|name|message  per check. Never prints a value.
check_env_file() {
  local f="$1" p=0 k v re perms
  if [ ! -f "$f" ]; then echo "problem|file|env file not found: $f"; return 1; fi
  for k in $REQUIRED; do
    v=$(envval "$f" "$k")
    if [ -n "$v" ]; then echo "ok|$k|is set"; else echo "problem|$k|is missing or empty"; p=$((p + 1)); fi
  done
  while IFS=$'\t' read -r k re; do
    [ -n "$k" ] || continue
    v=$(envval "$f" "$k")
    [ -n "$v" ] || continue
    if printf '%s' "$v" | grep -Eq -- "$re"; then echo "ok|$k|matches the rule for this environment"
    else echo "problem|$k|does not match the rule for this environment"; p=$((p + 1)); fi
  done <<< "$RULES"
  perms=$(stat -c %a "$f" 2>/dev/null || stat -f %Lp "$f" 2>/dev/null || echo "?")
  if [ "$perms" = "$ENV_MODE" ]; then echo "ok|permissions|file mode $perms"; else echo "warn|permissions|file mode is $perms, expected $ENV_MODE"; fi
  return "$p"
}

# ---------------- modes ----------------

case "$MODE" in

probe)   # facts about this machine, one key=value per line
  # shellcheck disable=SC1091
  if [ -r /etc/os-release ]; then . /etc/os-release; fi
  echo "os=${PRETTY_NAME:-$(uname -s)}"
  echo "kernel=$(uname -s)"
  echo "arch=$(uname -m)"
  echo "user=$(id -un)"
  echo "home=$HOME"
  for c in bash tar curl git rsync docker podman pm2 node systemctl nginx caddy; do
    if command -v "$c" >/dev/null 2>&1; then
      # a program that is on the path but cannot run is as good as missing
      case "$c" in pm2|node) "$c" --version >/dev/null 2>&1 || { echo "tool.$c=no"; continue; } ;; esac
      echo "tool.$c=yes"
    else echo "tool.$c=no"; fi
  done
  if command -v docker >/dev/null 2>&1; then
    echo "ver.docker=$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ,)"
    if docker info >/dev/null 2>&1; then echo "docker.access=yes"; else echo "docker.access=no"; fi
    if docker compose version >/dev/null 2>&1; then echo "docker.compose=yes"; else echo "docker.compose=no"; fi
  fi
  if command -v podman >/dev/null 2>&1; then
    echo "ver.podman=$(podman --version 2>/dev/null | awk '{print $3}')"
    if podman info >/dev/null 2>&1; then echo "podman.access=yes"; else echo "podman.access=no"; fi
    if podman compose version >/dev/null 2>&1; then echo "podman.compose=yes"; else echo "podman.compose=no"; fi
    echo "podman.rootless=$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null || echo unknown)"
  fi
  if pm2 --version >/dev/null 2>&1; then echo "ver.pm2=$(pm2 --version 2>/dev/null | tail -1)"; fi
  if node --version >/dev/null 2>&1; then echo "ver.node=$(node --version 2>/dev/null)"; fi
  if command -v loginctl >/dev/null 2>&1; then echo "linger=$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)"; fi
  if [ -z "$SUDO" ]; then echo "sudo=no"; elif $SUDO true >/dev/null 2>&1; then echo "sudo=yes"; else echo "sudo=no"; fi
  for p in ${SHIPIT_PORTS:-}; do
    if ss -ltnH 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$p\$"; then echo "port.$p=used"; else echo "port.$p=free"; fi
  done
  if [ -n "$APP_DIR" ]; then
    if [ -d "$APP_DIR" ]; then
      if [ -d "$APP_DIR/releases" ] && [ -d "$APP_DIR/shared" ]; then echo "appdir.state=shipit"
      elif [ -z "$(ls -A "$APP_DIR" 2>/dev/null)" ]; then echo "appdir.state=empty"
      else echo "appdir.state=other"; fi
      if [ -w "$APP_DIR" ]; then echo "appdir.writable=yes"; else echo "appdir.writable=no"; fi
    else
      echo "appdir.state=missing"
      up="$APP_DIR"
      while [ ! -d "$up" ] && [ "$up" != "/" ]; do up=$(dirname "$up"); done
      if [ -w "$up" ]; then echo "appdir.writable=yes"; else echo "appdir.writable=no"; fi
    fi
  fi
  exit 0 ;;

prepare)
  mkdir -p "$REL_DIR" "$SHARED" || fail "Could not create $APP_DIR. Make the folder once yourself: sudo mkdir -p $APP_DIR && sudo chown \$USER $APP_DIR"
  check_runtime
  log "ready"
  exit 0 ;;

check-env)
  check_env_file "$ENV_REMOTE_PATH"; exit $? ;;

backup-env)
  if [ -f "$ENV_REMOTE_PATH" ]; then
    cp -p "$ENV_REMOTE_PATH" "$ENV_REMOTE_PATH.bak-$(date +%Y%m%d-%H%M%S)"
    log "backed up the old file"
  fi
  exit 0 ;;

deploy)
  REL="${SHIPIT_REL:?SHIPIT_REL is not set}"; NEW="$REL_DIR/$REL"
  [ -d "$NEW" ] || fail "The uploaded version is missing on the server: $NEW"
  RESULT="failed"
  trap 'if [ "$RESULT" != ok ]; then record "$REL" "$VERSION_LABEL" "$RESULT" || true; fi' EXIT
  cd "$NEW"

  if [ -n "$ENV_LINK_AS" ]; then
    [ -f "$ENV_REMOTE_PATH" ] || fail "There is no settings file on the server at $ENV_REMOTE_PATH. Send it with: shipit secrets push"
    chmod "$ENV_MODE" "$ENV_REMOTE_PATH" 2>/dev/null || true
    ln -sfn "$ENV_REMOTE_PATH" "$NEW/$ENV_LINK_AS"
  fi

  for d in $PRESERVE; do
    mkdir -p "$SHARED/$d"
    if [ -d "$NEW/$d" ] && [ ! -L "$NEW/$d" ]; then cp -an "$NEW/$d/." "$SHARED/$d/" 2>/dev/null || true; rm -rf "${NEW:?}/$d"; fi
    ln -sfn "$SHARED/$d" "$NEW/$d"
  done

  begin build
  build_release
  finish build

  if [ -n "$PRE_SWITCH" ]; then
    begin migrate
    log "Running: $PRE_SWITCH"
    bash -c "$PRE_SWITCH" </dev/null || fail "The step before going live failed, so nothing was switched."
    finish migrate
  fi

  go_back() {   # put the previous version back after the new one failed
    if [ -n "$PREV" ] && [ -d "$REL_DIR/$PREV" ]; then
      log "Going back to $(ver_of "$PREV")"
      point_current "$PREV"; activate || true
      if healthy; then log "$(ver_of "$PREV") is serving again."; else log "WARNING: $(ver_of "$PREV") is not healthy either."; fi
    else
      log "There is no earlier version to go back to."
    fi
  }

  begin live
  PREV="$(live_rel || true)"
  log "Switching from $(ver_of "${PREV:-none-none-nothing}") to $VERSION_LABEL"
  point_current "$REL"
  if ! activate; then
    printf '@@fail live\n'
    log "The new version could not be started. Recent logs:"
    show_logs 40 || true
    go_back
    RESULT="failed"
    exit 3
  fi
  finish live

  begin health
  if healthy; then
    finish health
  else
    log "The new version did not become healthy. Recent logs:"
    show_logs 40 || true
    go_back
    RESULT="failed"
    printf '@@fail health\n'
    exit 3
  fi

  RESULT="ok"
  record "$REL" "$VERSION_LABEL" ok

  if [ -n "$POST_DEPLOY" ]; then
    ( cd "$CUR" && bash -c "$POST_DEPLOY" </dev/null ) || log "WARNING: the step after going live failed"
  fi

  # keep the newest $KEEP releases and always the live one
  n=0; LIVE="$(live_rel)"
  for r in $(releases | sort -r); do
    n=$((n + 1))
    [ "$r" = "$LIVE" ] && continue
    if [ "$n" -gt "$KEEP" ]; then cleanup_release "$REL_DIR/$r"; rm -rf -- "${REL_DIR:?}/$r"; log "Removed old version $r"; fi
  done
  exit 0 ;;

rollback)
  LIVE="$(live_rel || true)"; PREV=""; TO="${SHIPIT_TO:-}"
  while IFS='|' read -r _ts rel ver res; do
    [ "$res" = ok ] || continue
    [ "$rel" != "$LIVE" ] || continue
    [ -d "$REL_DIR/$rel" ] || continue
    if [ -n "$TO" ] && [ "$ver" != "$TO" ] && [ "$rel" != "$TO" ]; then continue; fi
    PREV="$rel"; PREV_VER="$ver"; break
  done < <(sort -r "$HIST" 2>/dev/null || true)
  if [ -z "$PREV" ]; then
    if [ -n "$TO" ]; then fail "There is no earlier version called $TO to go back to."; fi
    fail "There is no earlier version to go back to."
  fi
  begin live
  log "Going back from $(ver_of "${LIVE:-none-none-nothing}") to ${PREV_VER:-$PREV}"
  point_current "$PREV"; activate
  finish live
  begin health
  healthy || { printf '@@fail health\n'; fail "${PREV_VER:-$PREV} did not become healthy."; }
  finish health
  printf '@@result %s\n' "${PREV_VER:-$(ver_of "$PREV")}"
  exit 0 ;;

status)
  echo "live=$(live_rel || true)"
  echo "versions=$(releases | tr '\n' ' ')"
  if [ -f "$HIST" ]; then echo "last=$(tail -1 "$HIST")"; fi
  if running 2>/dev/null; then echo "running=yes"; else echo "running=no"; fi
  if [ -n "$HEALTH_URL" ]; then
    if curl -fsS -o /dev/null --max-time 5 "$HEALTH_URL" 2>/dev/null; then echo "health=ok"; else echo "health=failing"; fi
  fi
  exit 0 ;;

history)
  if [ -f "$HIST" ]; then sort -r "$HIST" | head -"${SHIPIT_LINES:-20}"; fi
  exit 0 ;;

logs)
  show_logs "${SHIPIT_LINES:-100}"; exit 0 ;;

*) fail "unknown mode $MODE" ;;
esac
