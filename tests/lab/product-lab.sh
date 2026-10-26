#!/usr/bin/env bash
# product-lab.sh — walk a product through the whole declarative surface, on a
# throwaway instance, and print what actually lands where.
#
# This is an EXPERIMENT surface, not a CI gate: it exists so a claim about
# product authoring can be checked against the running code before it is written
# into a doc. Every assertion here was written by running it first.
#
# It needs no docker, no root, no model credits — `arizuko create` and
# `arizuko products apply` are filesystem + SQLite only. It cannot run a turn;
# for that, see `make smoke` against a live instance.
#
# Usage:  tests/lab/product-lab.sh [workdir]
#         tests/lab/product-lab.sh --keep      # leave the instance for poking
set -euo pipefail

KEEP=0
[ "${1:-}" = "--keep" ] && { KEEP=1; shift; }
WORK="${1:-$(mktemp -d)}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"

export PREFIX="$WORK/prefix"
export HOST_APP_DIR="$WORK"
BIN="$WORK/arizuko"
INST=lab
DATA="$PREFIX/data/arizuko_$INST"

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()  { printf '   ok   %s\n' "$*"; }
bad() { printf '   FAIL %s\n' "$*"; exit 1; }

say "build + create instance"
go build -o "$BIN" "$REPO/cmd/arizuko"
# chown warnings are expected off-root and are not fatal: nothing here spawns a
# container, so the agent uid never needs to write these paths.
"$BIN" create "$INST" 2>/dev/null | tail -1
GROUP="$DATA/groups/main"

say "author a product (the corpus reference, copied out so you can edit it)"
PROD="$WORK/products/helpdesk"
mkdir -p "$(dirname "$PROD")"
cp -r "$REPO/ant/examples/helpdesk" "$PROD"
find "$PROD" -type f | sed "s|$PROD/|   |" | sort

say "apply it as a one-product mix"
printf 'packages:\n  - source: "%s"\n' "$PROD" > "$GROUP/arizuko.yml"
"$BIN" products "$INST" apply main 2>/dev/null | tail -2

say "what landed in the group home"
find "$GROUP" -type f | sed "s|$GROUP/|   |" | sort

say "the payload kinds that TRANSFORM (the two worth knowing)"
[ -f "$GROUP/.claude/skills/triage/SKILL.md" ] \
  && ok "skills/<n>/ relocated to .claude/skills/<n>/" \
  || bad "skill did not relocate"
grep -q 'arizuko:package:helpdesk BEGIN' "$GROUP/CLAUDE.md" \
  && ok "CLAUDE.md wrapped in its product's marked region" \
  || bad "CLAUDE.md region missing"

say "the installed-package record — (folder, name) is the lock"
sqlite3 "$DATA/store/routd/routd.db" \
  "select folder, name, manifest from installed_packages;" | sed 's/^/   /'

say "re-apply is idempotent"
out=$("$BIN" products "$INST" apply main 2>/dev/null)
case "$out" in
  *unchanged*) ok "second apply reports unchanged, writes nothing" ;;
  *)           bad "apply is not idempotent: $out" ;;
esac

say "payloads that REACH NO CONSUMER are refused, not silently seeded"
mkdir -p "$PROD/migrations"; echo x > "$PROD/migrations/001-x.md"
out=$("$BIN" products "$INST" apply main 2>&1 || true)
case "$out" in
  *"nothing runs a product"*) ok "migrations/ refused (spec 5/28)" ;;
  *)                          bad "migrations/ was accepted: $out" ;;
esac
rm -rf "$PROD/migrations"
printf '[[task]]\ncron = "0 9 * * *"\nprompt = "digest"\n' > "$PROD/tasks.toml"
out=$("$BIN" products "$INST" apply main 2>&1 || true)
case "$out" in
  *"nothing reads a product"*) ok "tasks.toml refused (BUGS L35)" ;;
  *)                           bad "tasks.toml was accepted: $out" ;;
esac
rm -f "$PROD/tasks.toml"
cp "$PROD/.claude/settings.json" "$WORK/s.bak"
echo '{"mcpServers":{"x":{"command":"y"}}}' > "$PROD/.claude/settings.json"
out=$("$BIN" products "$INST" apply main 2>&1 || true)
case "$out" in
  *"mcpServers reaches nothing"*) ok "mcpServers refused (BUGS J15)" ;;
  *)                              bad "mcpServers was accepted: $out" ;;
esac
cp "$WORK/s.bak" "$PROD/.claude/settings.json"

say "two-product mix — precedence per payload kind"
SECOND="$WORK/products/oncall"; mkdir -p "$SECOND/facts" "$SECOND/skills/triage"
printf 'name: oncall\n' > "$SECOND/arizuko.yml"
printf 'You are an on-call router. Terse. Always name the rota.\n' > "$SECOND/PERSONA.md"
printf '## Oncall rules\n\n- Page only during an active incident.\n' > "$SECOND/CLAUDE.md"
printf 'Escalate to the platform rota after 15 minutes.\n' > "$SECOND/facts/rota.md"
printf -- '---\nname: triage\ndescription: Oncall triage — severity first.\n---\nSeverity, then rota.\n' \
  > "$SECOND/skills/triage/SKILL.md"
printf 'packages:\n  - source: "%s"\n  - source: "%s"\n' "$PROD" "$SECOND" \
  > "$GROUP/arizuko.yml"
"$BIN" products "$INST" apply main 2>/dev/null | tail -3
grep -q 'support triage' "$GROUP/PERSONA.md" \
  && ok "PERSONA.md: FIRST provider wins (helpdesk), later warned" \
  || bad "persona precedence wrong"
grep -q 'Oncall triage' "$GROUP/.claude/skills/triage/SKILL.md" \
  && ok "skills: LAST provider wins wholesale (oncall)" \
  || bad "skill precedence wrong"
[ "$(grep -c 'arizuko:package:.* BEGIN' "$GROUP/CLAUDE.md")" = 2 ] \
  && ok "CLAUDE.md: one region per product, in mix order" \
  || bad "region count wrong"
ls "$GROUP/facts" | tr '\n' ' ' | sed 's/^/   facts: /'; echo

say "a filename collision in a union payload refuses the WHOLE apply"
cp "$PROD/facts/sla.md" "$SECOND/facts/sla.md"
out=$("$BIN" products "$INST" apply main 2>&1 || true)
case "$out" in
  *"refuse a filename collision"*) ok "collision refused, no half-composed group" ;;
  *)                               bad "collision was accepted: $out" ;;
esac
rm -f "$SECOND/facts/sla.md"

say "handing an asset between two products in one mix keeps it (BUGS L34)"
# The condition is precise: A recorded the asset, A stops shipping it, and B
# ships it with IDENTICAL bytes — so the on-disk hash still matches A's recorded
# hash. Against A's own hashes alone that reads as clean-and-no-longer-mine, and
# A deleted what B had just written. dropStale now takes the mix's UNION, so a
# path any current provider ships is still shipped. Both products must stay in
# the mix; dropping A entirely does not exercise it, because A's record is then
# never processed.
"$BIN" group "$INST" add web:l34 l34 >/dev/null 2>&1
LG="$DATA/groups/l34"
PA="$WORK/products/pa"; PB="$WORK/products/pb"
mkdir -p "$PA/skills/shared" "$PB/skills/shared"
printf 'name: pa\n' > "$PA/arizuko.yml"; printf 'name: pb\n' > "$PB/arizuko.yml"
printf -- '---\nname: shared\ndescription: shared skill.\n---\nBody.\n' > "$PA/skills/shared/SKILL.md"
cp "$PA/skills/shared/SKILL.md" "$PB/skills/shared/SKILL.md"
printf 'packages:\n  - source: "%s"\n' "$PA" > "$LG/arizuko.yml"
"$BIN" products "$INST" apply l34 >/dev/null 2>&1
[ -f "$LG/.claude/skills/shared/SKILL.md" ] || bad "setup: pa never owned the skill"
rm -rf "$PA/skills"
printf 'packages:\n  - source: "%s"\n  - source: "%s"\n' "$PA" "$PB" > "$LG/arizuko.yml"
out=$("$BIN" products "$INST" apply l34 2>&1)
[ -f "$LG/.claude/skills/shared/SKILL.md" ] \
  && ok "skill survived the handoff from pa to pb" \
  || bad "L34 reproduced: pb ships the skill, pa's dropStale deleted it"
case "$out" in
  *"edited locally"*) bad "handoff reported as a local edit: $out" ;;
  *)                  ok "no false 'edited locally' warning — ownership moved, the file did not" ;;
esac

say "a product ships its OWN live tests"
# A product carries its own cases; the harness reads them with --cases. Contract:
# specs/5/9 §"A product carries its own cases". `validate` is the free tier —
# `anteval run` spends model credits, so it is not run here.
[ -f "$GROUP/eval/helpdesk.toml" ] \
  && ok "product eval cases landed at <group>/eval/" || bad "eval cases missing"
if command -v go >/dev/null && [ -d "$REPO/anteval" ]; then
  go build -o "$WORK/anteval" "$REPO/anteval/cmd/anteval"
  out=$("$WORK/anteval" validate --cases "$GROUP/eval" 2>&1 || true)
  case "$out" in
    *"cases ok"*) ok "harness validates them from the group home: $out" ;;
    *)            bad "harness rejected the product's cases: $out" ;;
  esac

  # Every shipped product, not just the one seeded above. A suite that only the
  # reference product carries is a pattern nobody follows; container's
  # TestEveryProductShipsEvalCases locks that a suite EXISTS, and this checks the
  # harness actually accepts each one.
  missing=0; rejected=0; total=0
  for d in "$REPO"/ant/examples/*/; do
    n=$(basename "$d"); total=$((total + 1))
    if [ ! -d "$d/eval" ]; then
      bad "product $n ships no eval/ suite"; missing=$((missing + 1)); continue
    fi
    out=$("$WORK/anteval" validate --cases "$d/eval" 2>&1 || true)
    case "$out" in
      *"cases ok"*) : ;;
      *) bad "product $n has a suite the harness rejects: $out"; rejected=$((rejected + 1)) ;;
    esac
  done
  [ "$missing" = 0 ] && [ "$rejected" = 0 ] \
    && ok "all $total shipped products carry a suite the harness accepts"
fi

say "the OTHER half: a package (instance-wide), not a product (one group)"
# A product seeds one group's files. A package installs instance-wide assets and
# its record keys on folder '' to say so. It ships exactly two files, split by
# who owns the format: helpbot.yml is docker's compose fragment, helpbot.yaml is
# arizuko's 5/8 manifest — the same document `arizuko apply` takes, so a package
# can declare anything the manifest carries (BUGS L44).
PKG="$WORK/pkgs/helpbot"; mkdir -p "$PKG"
cat > "$PKG/helpbot.yml" <<'YML'
services:
  helpbot:
    image: alpine:3.20
    command: ["sleep", "3600"]
    environment:
      LISTEN_ADDR: ":8080"
YML
cat > "$PKG/arizuko.yml" <<'YAML'
name: helpbot
proxyd_routes:
  - path: /helpbot/
    backend: http://helpbot:8080
    auth: user
    strip_prefix: true
acl:
  - principal: folder:main
    action: mcp:send
    scope: main
    effect: allow
network_rules:
  - folder: main
    target: helpbot.example.com
YAML
# The summary names every grant: files and routes are inert until `generate`,
# a grant is live the moment it lands, so it is named and not counted (BUGS L36).
inst=$("$BIN" packages "$INST" install "$PKG" --folder main 2>/dev/null)
printf '%s\n' "$inst" | tail -2 | sed 's/^/   /'
case "$inst" in
  *"folder:main mcp:send on main"*) ok "install summary names the grant it applied" ;;
  *)                                bad "install summary hid the grant: $inst" ;;
esac
DB="$DATA/store/routd/routd.db"
q() { sqlite3 "$DB" "$1"; }
[ "$(q "select count(*) from acl where granted_by='package:helpbot';")" = 1 ] \
  && ok "grant applied, attributed to package:helpbot" || bad "grant missing"
[ "$(q "select count(*) from proxyd_routes where path='/helpbot/';")" = 1 ] \
  && ok "proxyd route row written" || bad "route missing"
# The row kind the old *-grants.json / *-routes.json pair could not express at
# all: the manifest carries it, so the package can ship it (BUGS L44).
[ "$(q "select count(*) from network_rules where folder='main' and target='helpbot.example.com';")" = 1 ] \
  && ok "egress rule installed — a kind the JSON sidecars could not declare" || bad "network rule missing"
[ "$(q "select folder from installed_packages where name='helpbot';")" = "" ] \
  && ok "record folder is '' — instance-wide, not a group" || bad "record folder wrong"

say "remove reverses exactly what the record names"
"$BIN" packages "$INST" remove helpbot 2>/dev/null | tail -1
[ "$(q "select count(*) from acl where granted_by='package:helpbot';")" = 0 ] \
  && ok "grant revoked" || bad "grant survived remove"
[ "$(q "select count(*) from proxyd_routes where path='/helpbot/';")" = 0 ] \
  && ok "route deleted" || bad "route survived remove"
[ "$(q "select count(*) from network_rules where target='helpbot.example.com';")" = 0 ] \
  && ok "egress rule withdrawn" || bad "network rule survived remove"
[ "$(q "select count(*) from installed_packages where name='helpbot';")" = 0 ] \
  && ok "record dropped" || bad "record survived remove"
[ -z "$(ls "$DATA/services" 2>/dev/null | grep helpbot || true)" ] \
  && ok "fragment files gone" || bad "fragment survived remove"

if [ "$KEEP" = 1 ]; then
  printf '\ninstance kept at %s\n' "$DATA"
else
  printf '\nlab complete (pass --keep to keep %s)\n' "$WORK"
fi
