#!/usr/bin/env bash
set -euo pipefail

deploy_path="${1:?usage: deploy-test.sh DEPLOY_PATH MALL_IMAGE_DIGEST MINIO_IMAGE_DIGEST DEPENDENCY_REGISTRY}"
export MALL_IMAGE="${2:?an immutable mall image digest is required}"
export MINIO_IMAGE="${3:?an immutable MinIO image digest is required}"
# <ACR_REGISTRY>/<ACR_NAMESPACE>: the mirrors of Docker Hub images referenced by
# docker-compose.registry.yml. Passed by CI so the server never has to reach a
# public registry; there is deliberately no default.
export DEPENDENCY_REGISTRY="${4:?the dependency registry (ACR registry and namespace) is required}"
for name in MALL_IMAGE MINIO_IMAGE; do
  [[ "${!name}" =~ ^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$ ]] || { echo "An immutable image digest is required: $name" >&2; exit 1; }
done
[[ "$DEPENDENCY_REGISTRY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9]+([_-][a-z0-9]+)*$ ]] ||
  { echo 'Invalid dependency registry' >&2; exit 1; }
release_dir="$(cd "$(dirname "$0")/.." && pwd)"
env_file="$deploy_path/.env"
[[ -f "$env_file" ]] || { echo "Create $env_file from deploy/test.env.example before deploying" >&2; exit 1; }
chmod 600 "$env_file"
# Also serialize manual deployments. util-linux/flock is a server prerequisite.
exec 9>"$deploy_path/.deploy.lock"
flock -n 9 || { echo 'Another test deployment is running' >&2; exit 1; }

compose=(
  docker compose --project-name ecommerce-test
  --project-directory "$release_dir" --env-file "$env_file"
  -f "$release_dir/docker-compose.yml" -f "$release_dir/docker-compose.test.yml"
  -f "$release_dir/docker-compose.caddy.yml" -f "$release_dir/docker-compose.registry.yml"
)
# !override in the overlay requires Compose >= 2.24.4. Fail before replacing
# any running service if configuration, registry access or either image is invalid.
"${compose[@]}" config --quiet
# Provision read-only ACR docker login as the SSH deployment user beforehand.
# Never forward CI push credentials to the server or build images here. The
# dependency mirrors are copied into ACR by CI before this script runs.
"${compose[@]}" pull mall minio caddy
# Validate the gateway BEFORE replacing running services. The frontend is deployed
# separately, so there is no local static root to check here. Do not print the
# interpolated Compose config: it contains runtime secrets.
"${compose[@]}" run --rm --no-deps caddy-check sh -ec '
  caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
'
if ! "${compose[@]}" up -d --wait --wait-timeout 180; then
  "${compose[@]}" ps
  "${compose[@]}" logs --tail=100 mall minio caddy
  exit 1
fi
# Verify the real TLS virtual hosts before the hop that depends on them. First
# boot also issues certificates, so allow more time than a plain container start.
# Docker network aliases keep this traffic local while still verifying the public
# certificate chain. Console may intentionally answer 403 for unlisted source IPs.
# There is no frontend web virtual host: it is deployed separately.
# shellcheck disable=SC2016
if ! "${compose[@]}" exec -T caddy sh -ec '
  for attempt in $(seq 1 60); do
    if wget -S -O /dev/null -T 5 "https://$API_DOMAIN/" 2>&1 | grep -q "HTTP/1.1 404" &&
       wget -S -O /dev/null -T 5 "https://$CONSOLE_DOMAIN/" 2>&1 | grep -Eq "HTTP/1.1 (200|403)" &&
       wget -q -O /dev/null -T 5 http://minio:9001/; then exit 0; fi
    sleep 2
  done
  exit 1
'; then
  echo 'Deployment failed: Caddy HTTPS/API or MinIO Console is not ready; check DNS, ACME, ports 80/443' >&2
  "${compose[@]}" logs --tail=100 caddy mall
  exit 1
fi
# The backend reaches storage only through that same Caddy listener, with TLS and
# certificate verification. MinIO is not a CI fixture: probe without writing data.
# These variables must expand inside the container, not on the host.
# shellcheck disable=SC2016
if ! "${compose[@]}" exec -T mall sh -ec '
  scheme=http
  [ "$STORAGE_USE_TLS" != true ] || scheme=https
  for attempt in $(seq 1 30); do
    if wget -q -O /dev/null -T 5 "$scheme://$STORAGE_ENDPOINT/minio/health/ready"; then exit 0; fi
    sleep 2
  done
  exit 1
'; then
  echo 'Deployment failed: the backend cannot reach the storage API through Caddy' >&2
  exit 1
fi
printf '%s\n' "$MALL_IMAGE" > "$release_dir/image"
printf '%s\n' "$MINIO_IMAGE" > "$release_dir/minio-image"
# Keep successful release metadata for inspection/manual rollback. Do not prune
# images, remove volumes, or automatically roll back database migrations.
ln -sfn "$release_dir" "$deploy_path/current"
echo "Deployed mall=$MALL_IMAGE minio=$MINIO_IMAGE"
