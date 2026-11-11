#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

mirror_one() {
  local source="$1" mirror="$2" destination digest reference
  destination="$DEPENDENCY_REGISTRY/$mirror"
  # Mirror only the platform the server runs, so CI does not copy foreign
  # layers. Images without an index are single-platform and are copied as-is.
  digest="$(docker buildx imagetools inspect --raw "$source" |
    jq -er '(.manifests // []) | map(select(.platform.os == "linux" and .platform.architecture == "amd64")) | first | .digest')" || digest=''
  reference="$source"
  [[ -z "$digest" ]] || reference="${source%:*}@$digest"
  echo "Mirroring $reference -> $destination"
  docker buildx imagetools create --tag "$destination" "$reference"
}

# Re-entry used by xargs to copy several images at once.
if [[ "${1:-}" == "--mirror-one" ]]; then
  [[ $# -eq 3 ]] || { echo 'usage: mirror-images.sh --mirror-one SOURCE MIRROR' >&2; exit 2; }
  mirror_one "$2" "$3"
  exit 0
fi

: "${DEPENDENCY_REGISTRY:?set DEPENDENCY_REGISTRY to <ACR_REGISTRY>/<ACR_NAMESPACE>}"
# The value becomes an image reference and a shell argument on the server.
[[ "$DEPENDENCY_REGISTRY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9]+([_-][a-z0-9]+)*$ ]] ||
  { echo 'Invalid DEPENDENCY_REGISTRY' >&2; exit 1; }
command -v jq >/dev/null || { echo 'jq is required to select the linux/amd64 manifest' >&2; exit 1; }
parallel="${MIRROR_PARALLEL:-4}"
[[ "$parallel" =~ ^[1-9][0-9]?$ ]] || { echo 'MIRROR_PARALLEL must be 1-99' >&2; exit 1; }

# The test server cannot reach Docker Hub. Skip every mirror that already
# exists, so only the first release (or a version bump) transfers images and a
# Docker Hub outage cannot block deployments once the mirrors are populated.
missing=()
while read -r source mirror || [[ -n "${source:-}" ]]; do
  [[ -n "$source" && "$source" != \#* ]] || continue
  [[ "$source" =~ ^[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9._-]+$ ]] || { echo "Invalid source image: $source" >&2; exit 1; }
  [[ "$mirror" =~ ^[a-z0-9]+(-[a-z0-9]+)*:[A-Za-z0-9._-]+$ ]] || { echo "Invalid mirror repository: $mirror" >&2; exit 1; }
  if docker buildx imagetools inspect "$DEPENDENCY_REGISTRY/$mirror" >/dev/null 2>&1; then
    echo "Already mirrored: $DEPENDENCY_REGISTRY/$mirror"
    continue
  fi
  missing+=("$source" "$mirror")
done < deploy/mirror-images.txt

((${#missing[@]})) || { echo 'All dependency mirrors already exist'; exit 0; }

# The first release is bandwidth-bound (the runner uploads to ACR across the
# public internet), so copy several images concurrently.
printf '%s\n' "${missing[@]}" |
  xargs -P "$parallel" -n 2 "$0" --mirror-one
