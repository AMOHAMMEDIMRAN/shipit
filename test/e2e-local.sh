#!/usr/bin/env bash
# End-to-end test of shipit against this computer (no server, no containers needed).
# Deploys a tiny web app three times (good, good, broken) and checks that shipit
# switches versions, puts the old one back when the new one fails, rolls back on
# request, and never uploads local env files.
#
#   test/e2e-local.sh          builds shipit, runs everything in a temp folder, cleans up
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
T="$(mktemp -d)"
SH="$T/shipit"
PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
export SHIPIT_CONFIG_DIR="$T/cfg"
pass=0; failed=0
ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { failed=$((failed+1)); printf '  FAIL  %s\n' "$1"; }
check(){ if eval "$2"; then ok "$1"; else bad "$1"; fi; }
cleanup() { [ -f "$T/srv/pid" ] && kill "$(cat "$T/srv/pid")" 2>/dev/null || true; rm -rf "$T"; }
trap cleanup EXIT

(cd "$HERE" && go build -o "$SH" ./cmd/shipit)
mkdir -p "$T/proj" "$T/cfg" "$T/srv"; cd "$T/proj"
git init -q -b main . && git config user.email t@t && git config user.name t
cat > start.sh <<SCRIPT
#!/usr/bin/env bash
[ -f "$T/srv/pid" ] && kill "\$(cat "$T/srv/pid")" 2>/dev/null
cd "\$(pwd -P)"
[ -f BROKEN ] && exit 0
nohup python3 -m http.server $PORT --bind 127.0.0.1 >/dev/null 2>&1 &
echo \$! > "$T/srv/pid"
SCRIPT
chmod +x start.sh
echo "v1" > index.html
echo "SECRET=local-only" > .env.local
printf 'PAYMENT_KEY=rzp_live_abc\n' > .env.production
git add start.sh index.html && git commit -qm v1 && git tag v1.0.0
printf '[here]\ntype = local\n' > "$T/cfg/servers.conf"
mkdir .shipit
cat > .shipit/production.conf <<CONF
project = e2e
server = here
runtime = script
app_dir = $T/srv/e2e
health = http://127.0.0.1:$PORT/index.html
health_timeout = 6
start_cmd = ./start.sh
env_file = .env.production
require = PAYMENT_KEY
[rules]
PAYMENT_KEY = ^rzp_live_
CONF
page() { curl -s "http://127.0.0.1:$PORT/index.html"; }

echo "shipit end-to-end (local):"
check "deploy without a settings file on the server is refused" '! "$SH" deploy production --yes >/dev/null 2>&1'
check "settings are sent" '"$SH" secrets push production --yes >/dev/null 2>&1'
check "server copy is private (mode 600)" '[ "$(stat -c %a "$T/srv/e2e/shared/.env")" = 600 ]'
check "first deploy works" '"$SH" deploy production --yes >/dev/null 2>&1 && [ "$(page)" = v1 ]'
echo "v2" > index.html; git commit -qam v2; git tag v1.1.0
check "deploy by tag works" '"$SH" deploy production --tag v1.1.0 --yes >/dev/null 2>&1 && [ "$(page)" = v2 ]'
touch BROKEN; git add -f BROKEN; echo "v3" > index.html; git commit -qam v3; git tag v1.2.0
check "a version that never gets healthy is refused" '! "$SH" deploy production --tag v1.2.0 --yes >/dev/null 2>&1'
check "...and the previous version is serving again" '[ "$(page)" = v2 ]'
check "...and status says v1.1.0 is live" '"$SH" status production 2>&1 | grep -q "v1.1.0 is live"'
check "rollback skips the failed version" '"$SH" rollback production --yes >/dev/null 2>&1 && [ "$(page)" = v1 ]'
check "rollback to a named version works" '"$SH" rollback production --to v1.1.0 --yes >/dev/null 2>&1 && [ "$(page)" = v2 ]'
check "rollback to a missing version is refused" '! "$SH" rollback production --to v9.9.9 --yes >/dev/null 2>&1'
check "local env files were never uploaded" '[ -z "$(find "$T/srv/e2e/releases" -name ".env.local" -o -name ".env.production")" ]'
check "a production key rule stops a test key" 'printf "PAYMENT_KEY=rzp_test_x\n" > .env.production && ! "$SH" secrets push production --yes >/dev/null 2>&1'

# ---- uncommitted changes ----
git rm -q BROKEN
echo "v4" > index.html
echo "release notes" > notes.txt
printf 'PAYMENT_KEY=rzp_live_abc\n' > .env.production
before=$(git rev-parse HEAD)
check "uncommitted changes are not deployed without a yes" '! "$SH" deploy production --yes 2>"$T/err" >/dev/null && grep -q -- "--commit" "$T/err" && [ "$(git rev-parse HEAD)" = "$before" ]'
check "--commit commits with a written message and deploys" '"$SH" deploy production --commit --yes >/dev/null 2>&1 && [ "$(git rev-parse HEAD)" != "$before" ] && [ "$(page)" = v4 ]'
check "...the message names the changes" 'git log -1 --format=%B | grep -q "notes.txt"'
check "...the new file was included" 'git ls-files | grep -qx notes.txt'
check "...the settings file was NOT committed" '! git ls-files | grep -q "^.env.production$" && ! git show --stat HEAD | grep -q "env.production"'
echo "v5" > index.html
check "shipit commit -m uses your message" '"$SH" commit -m "Change greeting to v5" --yes >/dev/null 2>&1 && [ "$(git log -1 --format=%s)" = "Change greeting to v5" ]'
check "nothing left to commit afterwards" '"$SH" commit --yes 2>&1 | grep -q "Nothing to commit"'

echo "$pass passed, $failed failed"
[ "$failed" -eq 0 ]
