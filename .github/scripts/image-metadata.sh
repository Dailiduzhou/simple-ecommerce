#!/usr/bin/env bash
set -euo pipefail

: "${ACR_REGISTRY:?set ACR_REGISTRY to the public registry hostname}"
: "${GITHUB_SHA:?a full commit SHA is required}"
: "${GITHUB_OUTPUT:?GitHub output file is required}"
namespace="${ACR_NAMESPACE:-${NAMESPACE:-}}"
# The ACR namespace is deployment configuration; there is no vendor default here.
[[ -n "$namespace" ]] || { echo 'Set ACR_NAMESPACE (or NAMESPACE) to the ACR namespace' >&2; exit 1; }
# Reject protocols, paths, whitespace and output-file injection. The registry
# and namespace are configuration, not arbitrary shell fragments.
[[ "$ACR_REGISTRY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?$ ]] || { echo 'Invalid ACR registry hostname' >&2; exit 1; }
[[ "$namespace" =~ ^[a-z0-9]+([_-][a-z0-9]+)*$ ]] || { echo 'Invalid ACR namespace' >&2; exit 1; }
[[ "$GITHUB_SHA" =~ ^[a-f0-9]{40}$ ]] || { echo 'Expected a full 40-character commit SHA' >&2; exit 1; }

# Intentionally the LAST eight characters, not git rev-parse --short's prefix.
# Different component prefixes are essential when sharing a single repository.
short_sha="${GITHUB_SHA: -8}"
repository="$ACR_REGISTRY/$namespace/aiyoung"
# Dependency mirrors live beside mall and MinIO in the same ACR namespace.
printf 'name=%s\nregistry=%s\ndependency_registry=%s/%s\nmall_tag=%s:mall-%s\nminio_tag=%s:minio-%s\n' \
  "$repository" "$ACR_REGISTRY" "$ACR_REGISTRY" "$namespace" \
  "$repository" "$short_sha" "$repository" "$short_sha" >> "$GITHUB_OUTPUT"
