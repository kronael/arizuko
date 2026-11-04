#!/usr/bin/env bash
# digest-work: GitHub work in the window.
# Source order: live gh, then git log on the clone, then the message store.
# usage: gather_github.sh <run-dir>   (reads <run-dir>/window.txt)
# writes: gh_merged.json gh_open.json gh_carried.json gh_releases.json
#         commits.txt github.md gh_source.txt
. "$(dirname "$0")/common.sh"
OUTDIR="${1:?usage: gather_github.sh <run-dir>}"
. "$OUTDIR/window.txt"
CLONE="$("$CFG" clone)"  # a local clone of $REPO; commits come from it
cd "$OUTDIR"
SRC=""
DAY="${START%%T*}"
GIT_CRED=(-c credential.helper= -c 'credential.helper=!gh auth git-credential')

has_gh() {
  gh auth status >/dev/null 2>&1
}

# select_window <field>: keep items whose <field> is inside START..END
select_window() {
  jq --arg s "$START" --arg e "$END" --arg f "$1" \
    '[.[] | select(.[$f] >= $s and .[$f] <= $e)]'
}

if ! has_gh && [[ -n "${ARIZUKO_GH_WRITE:-}" && -z "${GH_TOKEN:-}" ]]
then
  export GH_TOKEN="$ARIZUKO_GH_WRITE"
fi

if has_gh
then
  SRC="gh ($(gh api user --jq .login))"
  gh pr list --repo "$REPO" --state merged --search "merged:>=$DAY" \
    --json number,title,url,author,files,mergedAt,createdAt --limit 200 \
    | select_window mergedAt > gh_merged.json
  gh pr list --repo "$REPO" --state open --search "created:>=$DAY" \
    --json number,title,url,author,files,createdAt --limit 100 \
    | select_window createdAt > gh_open.json
  # carried: still open, opened before START
  gh pr list --repo "$REPO" --state open --limit 200 \
    --json number,title,url,author,files,createdAt \
    | jq --arg s "$START" '[.[] | select(.createdAt < $s)]' > gh_carried.json
  gh release list --repo "$REPO" --limit 100 \
    --json tagName,name,publishedAt \
    | select_window publishedAt > gh_releases.json
else
  for f in gh_merged.json gh_open.json gh_carried.json gh_releases.json
  do
    echo '[]' > "$f"
  done
fi

# pr_scope.json: number -> {scope, url, title}. scope = the title's conventional-commit
# scope, else the top-level directory most changed files live in. Covers every PR the
# digest may cite: merged / open / carried, plus all PRs created in the window (any state).
if has_gh
then
  gh pr list --repo "$REPO" --state all --search "created:>=$DAY" \
    --json number,title,url,files --limit 200 > gh_created_all.json
else
  echo '[]' > gh_created_all.json
fi
jq -s 'add | unique_by(.number) | map({key: (.number | tostring), value: {url: .url, title: .title,
  scope: ((.title | [match("^[a-z]+\\(([^)]+)\\)")] | .[0].captures[0].string)
          // ((.files // []) | map(.path | split("/")[0])
              | if length == 0 then "repo" else (group_by(.) | max_by(length) | .[0]) end))}})
  | from_entries' gh_merged.json gh_open.json gh_carried.json gh_created_all.json > pr_scope.json

# commits on the default branch; the remote URL carries no token, gh does
: > commits.txt
if [[ -d "$CLONE/.git" ]]
then
  if git -C "$CLONE" "${GIT_CRED[@]}" fetch -q origin 2>fetch.err
  then
    branch=$(git -C "$CLONE" "${GIT_CRED[@]}" ls-remote --symref origin HEAD \
      | awk '/^ref:/ {sub("refs/heads/", "", $2); print $2}')
    SRC="${SRC:+$SRC + }git fetch"
  else
    branch=$(git -C "$CLONE" symbolic-ref --short refs/remotes/origin/HEAD)
    branch="${branch#origin/}"
    SRC="${SRC:+$SRC + }git (stale clone: $(head -c 120 fetch.err | tr '\n' ' '))"
  fi
  git -C "$CLONE" log "origin/$branch" --since="$START" --until="$END" \
    --format='%h|%cI|%an|%s' > commits.txt
fi

# message store, only when gh and the clone both gave nothing
if [[ "$(jq length gh_merged.json)" == 0 && ! -s commits.txt ]]
then
  session="gg$$"
  mcp_open "$session"
  trap 'mcp_close "$session"' EXIT
  mcp_call "$session" find_messages query:='"\"merged PR\""' scope:="\"$("$CFG" agent_folder)\"" \
    since:="\"$START\"" limit:=200 \
    | jq --arg e "$END" '[.messages[]? | select(.timestamp <= $e)
        | .content | gsub("«|»"; "")
        | capture("merged PR #(?<number>[0-9]+): (?<title>.*)")
        | .number |= tonumber]' > gh_merged.json
  SRC="${SRC:+$SRC + }store(find_messages)"
fi
echo "$SRC" > gh_source.txt

{
  echo "# GitHub — $REPO — $LABEL (source: $SRC)"
  echo
  echo "## merged PRs ($(jq length gh_merged.json))"
  jq -r '.[] | "- #\(.number) \(.title) — \(.author.login // .author // "?") \(.mergedAt // "")"' \
    gh_merged.json
  echo
  echo "## opened PRs ($(jq length gh_open.json))"
  jq -r '.[] | "- #\(.number) \(.title) — \(.author.login // "?")"' gh_open.json
  echo
  echo "## delta vs last period"
  echo "finished (merged, opened BEFORE start): $(jq --arg s "$START" \
    '[.[] | select((.createdAt // "") != "" and .createdAt < $s)] | length' gh_merged.json)"
  jq -r --arg s "$START" \
    '.[] | select((.createdAt // "") != "" and .createdAt < $s) | "- done #\(.number) \(.title)"' \
    gh_merged.json
  echo "started+finished (opened and merged in window): $(jq --arg s "$START" \
    '[.[] | select((.createdAt // "") >= $s)] | length' gh_merged.json)"
  echo "started, still open: $(jq length gh_open.json)"
  jq -r '.[] | "- new #\(.number) \(.title)"' gh_open.json
  echo "carried, still open from before: $(jq length gh_carried.json)"
  jq -r '.[] | "- open #\(.number) \(.title) (since \(.createdAt[0:10]))"' gh_carried.json
  echo
  echo "## releases ($(jq length gh_releases.json))"
  jq -r '.[] | "- \(.tagName) \(.publishedAt)"' gh_releases.json
  echo
  echo "## commits on default branch ($(wc -l < commits.txt))"
  awk -F'|' '{printf "- %s %s %s %s\n", $1, substr($2, 1, 16), $3, $4}' commits.txt
  echo
  echo "## by author"
  {
    jq -r '.[] | (.author.login // .author // "?")' gh_merged.json
    awk -F'|' '{print $3}' commits.txt
  } | sort | uniq -c | sort -rn | awk '{print "- " $2 ": " $1}'
} > github.md
echo "source=$SRC merged=$(jq length gh_merged.json) open=$(jq length gh_open.json)" \
  "releases=$(jq length gh_releases.json) commits=$(wc -l < commits.txt)"
