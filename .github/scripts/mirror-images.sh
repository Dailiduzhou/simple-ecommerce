#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${DEPENDENCY_REGISTRY:?set DEPENDENCY_REGISTRY to <ACR_REGISTRY>/<ACR_NAMESPACE>}"
# The value becomes an image reference and a shell argument on the server.
[[ "$DEPENDENCY_REGISTRY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9]+([_-][a-z0-9]+)*$ ]] ||
  { echo 'Invalid DEPENDENCY_REGISTRY' >&2; exit 1; }
command -v jq >/dev/null || { echo 'jq is required to select the linux/amd64 manifest' >&2; exit 1; }

# The test server is linux/amd64 and cannot reach Docker Hub. Copy every pinned
# dependency into the deployment ACR repository, and skip it once the mirror
# exists: after the first release, a Docker Hub outage cannot block deployments.
while read -r source mirror || [[ -n "${source:-}" ]]; do
  [[ -n "$source" && "$source" != \#* ]] || continue
  [[ "$source" =~ ^[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9._-]+$ ]] || { echo "Invalid source image: $source" >&2; exit 1; }
  [[ "$mirror" =~ ^[a-z0-9]+(-[a-z0-9]+)*:[A-Za-z0-9._-]+$ ]] || { echo "Invalid mirror repository: $mirror" >&2; exit 1; }
  destination="$DEPENDENCY_REGISTRY/$mirror"
  if docker buildx imagetools inspect "$destination" >/dev/null 2>&1; then
    echo "Already mirrored: $destination"
    continue
  fi
  # Mirror only the platform the server runs, so CI does not copy foreign
  # layers. Images without an index are single-platform and are copied as-is.
  digest="$(docker buildx imagetools inspect --raw "$source" |
    jq -er '(.manifests // []) | map(select(.platform.os == "linux" and .platform.architecture == "amd64")) | first | .digest')" || digest=''
  reference="$source"
  [[ -z "$digest" ]] || reference="${source%:*}@$digest"
  echo "Mirroring $reference -> $destination"
  docker buildx imagetools create --tag "$destination" "$reference"
done < deploy/mirror-images.txt
