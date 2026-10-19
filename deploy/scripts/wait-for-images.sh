#!/usr/bin/env bash
# Waits for the platform and runtime images of one tag to exist in the registry
# and prints their references, pinned by digest, as NAME=value lines on stdout
# (everything else goes to stderr), ready for deploy/scripts/smoke-kind.sh:
#
#   PLATFORM_IMAGE=ghcr.io/ncecere/pi-durable-platform:sha-0123456789ab@sha256:...
#   RUNTIME_IMAGE=ghcr.io/ncecere/pi-durable-platform-runtime:sha-0123456789ab@sha256:...
#
# Usage: wait-for-images.sh <tag>          (deploy.yml appends stdout to $GITHUB_ENV)
#
# The images are those .github/workflows/images.yml pushes for every branch
# commit (tag sha-<first 12 characters of the commit>), built a few minutes after
# the push, so the smoke test of a commit has to wait for them. The registry is
# read anonymously (`docker buildx imagetools inspect`), so the images must be
# public; no login is needed.
#
# Environment:
#   IMAGES_REGISTRY  registry and owner (default ghcr.io/ncecere)
#   WAIT_TIMEOUT     seconds to wait (default 1200; 0 checks once)
#   WAIT_INITIAL_INTERVAL, WAIT_MAX_INTERVAL  seconds between checks: starts at
#                    10 and grows by half each time up to 60
#   COMMIT_SHA, GITHUB_REPOSITORY, GITHUB_TOKEN  optional: with the first two the
#                    script looks up the images workflow's run for the commit, to
#                    stop waiting as soon as that run has failed or was cancelled
#                    and to point at it in the error (GITHUB_TOKEN is only needed
#                    for private repositories and rate limits)
#
# Exit status: 0 when both images exist, 1 when they do not (in time).
set -euo pipefail

registry=${IMAGES_REGISTRY:-ghcr.io/ncecere}
image_names=(pi-durable-platform pi-durable-platform-runtime)
image_vars=(PLATFORM_IMAGE RUNTIME_IMAGE)

log() { printf '%s\n' "$*" >&2; }
fail() {
  if [ "${GITHUB_ACTIONS:-}" = true ]; then printf '::error title=Images are missing::%s\n' "$*" >&2; else printf 'FAIL: %s\n' "$*" >&2; fi
  exit 1
}

# digest_of <reference>: prints the digest of the image (the manifest or index
# the tag points to), fails when the tag does not exist or the registry cannot
# be read.
digest_of() {
  local out
  out=$(docker buildx imagetools inspect --format '{{json .Manifest}}' "$1" 2>/dev/null) || return 1
  out=$(jq -r '.digest // empty' <<<"$out") || return 1
  [[ $out =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  printf '%s' "$out"
}

# images_run: prints "<status> <conclusion> <url>" of the newest push run of
# images.yml for $COMMIT_SHA; fails when unknown (no commit, no run yet, API
# error). A best-effort hint, never the reason to fail on its own.
images_run() {
  [ -n "${COMMIT_SHA:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ] || return 1
  local api=${GITHUB_API_URL:-https://api.github.com} auth=() out
  [ -z "${GITHUB_TOKEN:-}" ] || auth=(-H "Authorization: Bearer $GITHUB_TOKEN")
  out=$(curl -fsS --max-time 20 ${auth[@]+"${auth[@]}"} -H 'Accept: application/vnd.github+json' \
    "$api/repos/$GITHUB_REPOSITORY/actions/workflows/images.yml/runs?head_sha=$COMMIT_SHA&per_page=20" 2>/dev/null |
    jq -r 'first(.workflow_runs[] | select(.event == "push")) | "\(.status) \(.conclusion // "-") \(.html_url)"' 2>/dev/null) || return 1
  [ -n "$out" ] || return 1
  printf '%s' "$out"
}

# run_hint: a sentence that points at the images workflow run of the commit.
run_hint() {
  local run
  if run=$(images_run); then
    printf 'The images workflow run for this commit: %s (%s).' "${run##* }" "${run%% *}"
  elif [ -n "${GITHUB_REPOSITORY:-}" ]; then
    printf 'No images workflow run was found for this commit; see https://github.com/%s/actions/workflows/images.yml.' "$GITHUB_REPOSITORY"
  fi
}

# wait_for <tag>: polls until both images exist; sets the global `digests`.
wait_for() {
  local tag=$1 timeout=${WAIT_TIMEOUT:-1200} interval=${WAIT_INITIAL_INTERVAL:-10}
  local max=${WAIT_MAX_INTERVAL:-60} start=$SECONDS elapsed i run left missing state conclusion
  digests=("" "")
  while :; do
    for i in 0 1; do
      [ -n "${digests[i]}" ] || digests[i]=$(digest_of "$registry/${image_names[i]}:$tag" || true)
    done
    [ -z "${digests[0]}" ] || [ -z "${digests[1]}" ] || return 0

    missing=""
    for i in 0 1; do [ -n "${digests[i]}" ] || missing="$missing $registry/${image_names[i]}:$tag"; done
    elapsed=$((SECONDS - start))

    # The images run is over and did not succeed: the images will not come.
    if run=$(images_run); then
      read -r state conclusion _ <<<"$run"
      if [ "$state" = completed ] && [ "$conclusion" != success ]; then
        fail "the images workflow run for this commit ended with '$conclusion', so these images were not published:$missing. $(run_hint)"
      fi
    fi

    left=$((timeout - elapsed))
    if [ "$left" -le 0 ]; then
      fail "not found after ${elapsed}s:$missing. They are pushed by the images workflow for every branch commit (not for forks or tags): was it skipped or is it still running? $(run_hint)"
    fi
    log "waiting for$missing (${elapsed}s of ${timeout}s)"
    [ "$interval" -le "$left" ] || interval=$left
    sleep "$interval"
    interval=$((interval + interval / 2))
    [ "$interval" -le "$max" ] || interval=$max
  done
}

main() {
  [ $# -eq 1 ] && [ -n "$1" ] || { log "usage: $0 <tag>"; exit 2; }
  local tag=$1 i
  command -v docker >/dev/null || fail "docker is needed"
  command -v jq >/dev/null || fail "jq is needed"
  wait_for "$tag"
  for i in 0 1; do
    log "found $registry/${image_names[i]}:$tag@${digests[i]}"
    printf '%s=%s/%s:%s@%s\n' "${image_vars[i]}" "$registry" "${image_names[i]}" "$tag" "${digests[i]}"
  done
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
