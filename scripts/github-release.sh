#!/usr/bin/env bash
# Creates (or updates) the GitHub Release for $TAG with notes from
# docs/releases/<base version>.md, both images' digests and verification
# steps, and attaches the release assets. For each image (pi-durable-platform,
# the server; pi-durable-platform-runtime, the agent runtime):
#   <image>-<tag>-<os>-<arch>.sbom.spdx.json  the image's SPDX SBOM per platform
#                                             (BuildKit attestations)
#   <image>-<tag>.digest.txt                  the image reference by digest
# and checksums.txt, the SHA-256 of every file above.
# Run by the `release` job in .github/workflows/image.yml (needs GH_TOKEN, TAG,
# DIGEST (the server's index), RUNTIME_DIGEST (the runtime's) and read access
# to both images). Tags with a hyphen (v0.1.0-rc.1) become pre-releases and use
# the base version's notes. A missing notes file, SBOM or upload stops the
# script with a message saying what went wrong.
# RELEASE_DRY_RUN=1 writes the assets and notes to RELEASE_OUT (default: a
# temporary directory) and skips every GitHub call (gh is not needed). It still
# reads the SBOM attestations from the registry, so both digests must be
# published multi-arch images. To dry-run without them, RELEASE_SBOM_FILE and
# RELEASE_RUNTIME_SBOM_FILE (dry run only) name files holding the output of
#   docker buildx imagetools inspect <image>@<digest> --format '{{ json .SBOM }}'
# and the registry is not contacted. Only the SBOM source differs: tag and
# digest checks, notes, assets and checksums are the real code path.
set -euo pipefail

fail() {
  echo "::error::github-release: $*" >&2
  exit 1
}

: "${TAG:?TAG is not set}" "${DIGEST:?DIGEST is not set}" "${RUNTIME_DIGEST:?RUNTIME_DIGEST is not set}"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || fail "TAG is not a vX.Y.Z tag: '${TAG}'"
for d in "$DIGEST" "$RUNTIME_DIGEST"; do
  [[ "$d" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "not a sha256 digest: '${d}'"
done
dry_run="${RELEASE_DRY_RUN:-0}"
if [ "$dry_run" != 1 ] && { [ -n "${RELEASE_SBOM_FILE:-}" ] || [ -n "${RELEASE_RUNTIME_SBOM_FILE:-}" ]; }; then
  fail "RELEASE_SBOM_FILE and RELEASE_RUNTIME_SBOM_FILE are only for RELEASE_DRY_RUN=1"
fi
tools=(jq sha256sum)
{ [ -n "${RELEASE_SBOM_FILE:-}" ] && [ -n "${RELEASE_RUNTIME_SBOM_FILE:-}" ]; } || tools+=(docker)
[ "$dry_run" = 1 ] || tools+=(gh)
for cmd in "${tools[@]}"; do
  command -v "$cmd" >/dev/null || fail "${cmd} is not installed"
done

repo_slug=ncecere/pi-durable-platform
registry="ghcr.io/ncecere"
server_image="${registry}/pi-durable-platform"
runtime_image="${registry}/pi-durable-platform-runtime"
server_ref="${server_image}@${DIGEST}"
runtime_ref="${runtime_image}@${RUNTIME_DIGEST}"
base="${TAG%%-*}" # v0.1.0-rc.1 -> v0.1.0
notes_src="docs/releases/${base}.md"
[ -f "$notes_src" ] || fail "no release notes: ${notes_src}"
if [ "$dry_run" = 1 ]; then
  work="${RELEASE_OUT:-$(mktemp -d)}"
  mkdir -p "$work"
else
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
fi
assets="${work}/assets"
mkdir -p "$assets"
files=()

# ---- assets ------------------------------------------------------------------

# image_assets <name> <ref> <sbom file or empty>: the SBOM per platform and the
# digest file of one image.
image_assets() {
  local name="$1" ref="$2" sbom_file="$3"
  local sboms="${work}/${name}.sboms.json"
  if [ -n "$sbom_file" ]; then
    cp "$sbom_file" "$sboms" || fail "could not read ${sbom_file}"
  else
    docker buildx imagetools inspect "$ref" --format '{{ json .SBOM }}' >"$sboms" ||
      fail "could not read the SBOM attestations of ${ref}"
  fi
  # For a multi-platform index, .SBOM maps each platform ("linux/amd64") to its
  # attestations; a single-platform image has .SPDX at the top.
  if jq -e 'type == "object" and has("SPDX")' "$sboms" >/dev/null; then
    jq '{"image": .}' "$sboms" >"${sboms}.tmp" && mv "${sboms}.tmp" "$sboms"
  fi
  local platforms platform file
  platforms="$(jq -r 'if type == "object" then keys[] else empty end' "$sboms")"
  [ -n "$platforms" ] || fail "${ref} has no SBOM attestation (was it built with an SBOM attestation?)"
  while IFS= read -r platform; do
    file="${name}-${TAG}-${platform//\//-}.sbom.spdx.json" # linux/amd64 -> linux-amd64
    [ "$platform" = image ] && file="${name}-${TAG}.sbom.spdx.json"
    jq --arg p "$platform" '.[$p].SPDX' "$sboms" >"${assets}/${file}"
    jq -e '.spdxVersion | type == "string" and startswith("SPDX-")' "${assets}/${file}" >/dev/null ||
      fail "the SBOM of ${name} for ${platform} is not an SPDX document"
    files+=("$file")
  done <<<"$platforms"
  echo "$ref" >"${assets}/${name}-${TAG}.digest.txt"
  files+=("${name}-${TAG}.digest.txt")
}

image_assets pi-durable-platform "$server_ref" "${RELEASE_SBOM_FILE:-}"
image_assets pi-durable-platform-runtime "$runtime_ref" "${RELEASE_RUNTIME_SBOM_FILE:-}"
(cd "$assets" && sha256sum -- "${files[@]}" >checksums.txt)
files+=(checksums.txt)

# ---- notes -------------------------------------------------------------------

identity="https://github.com/${repo_slug}/.github/workflows/image.yml@refs/tags/${TAG}"
notes="${work}/notes.md"
{
  if [ "$TAG" != "$base" ]; then
    echo "> **Release candidate** for ${base}. Test it before relying on it; the final release follows."
    echo
  fi
  echo "## Images"
  echo
  echo "The platform (the Go server with the web UI):"
  echo
  echo '```'
  echo "${server_image}:${TAG}"
  echo "$server_ref"
  echo '```'
  echo
  echo "The agent runtime (one agent per container; the platform starts it):"
  echo
  echo '```'
  echo "${runtime_image}:${TAG}"
  echo "$runtime_ref"
  echo '```'
  echo
  echo "Deploy by digest. Verify the signatures with cosign v3 or newer (keyless, GitHub Actions OIDC):"
  echo
  echo '```sh'
  for ref in "$server_ref" "$runtime_ref"; do
    echo "cosign verify ${ref} \\"
    echo "  --certificate-identity ${identity} \\"
    echo "  --certificate-oidc-issuer https://token.actions.githubusercontent.com"
  done
  echo '```'
  echo
  echo "## Release assets"
  echo
  echo "- \`<image>-${TAG}-<os>-<arch>.sbom.spdx.json\`: each image's SBOM (SPDX JSON) for each platform, as attached to the image."
  echo "- \`<image>-${TAG}.digest.txt\`: each image's reference by digest."
  echo "- \`checksums.txt\`: SHA-256 of the files above (\`sha256sum -c checksums.txt\`)."
  echo
  echo "The SBOM and build provenance are also attached to the images as attestations:"
  echo "\`docker buildx imagetools inspect ${server_ref} --format '{{ json .SBOM }}'\`."
  echo
  # Relative links in docs/releases/ would break on the release page: point
  # them at the files as of this tag.
  blob="https://github.com/${repo_slug}/blob/${TAG}"
  sed -E \
    -e "s#\]\(\.\./\.\./#](${blob}/#g" \
    -e "s#\]\(\.\./#](${blob}/docs/#g" \
    -e "s#\]\((v[0-9]+\.[0-9]+\.[0-9]+\.md)#](${blob}/docs/releases/\1#g" \
    "$notes_src"
} >"$notes"

if [ "$dry_run" = 1 ]; then
  echo "dry run: notes ${notes}, assets in ${assets}"
  ls -l "$assets"
  exit 0
fi

# ---- release -----------------------------------------------------------------

# Name the repository: the asset upload runs from the assets directory, where
# gh can't infer it from a git checkout.
repo="${GITHUB_REPOSITORY:-$repo_slug}"
title="Pi Durable Platform ${TAG}"
if gh release view "$TAG" --repo "$repo" >/dev/null 2>&1; then
  gh release edit "$TAG" --repo "$repo" --title "$title" --notes-file "$notes" || fail "could not update the release ${TAG}"
else
  flags=(--repo "$repo" --title "$title" --notes-file "$notes" --verify-tag)
  [ "$TAG" != "$base" ] && flags+=(--prerelease)
  gh release create "$TAG" "${flags[@]}" || fail "could not create the release ${TAG}"
fi
(cd "$assets" && gh release upload "$TAG" --repo "$repo" --clobber "${files[@]}") || fail "could not upload the release assets"
echo "release ${TAG}: ${server_ref} ${runtime_ref}"
ls -l "$assets"
