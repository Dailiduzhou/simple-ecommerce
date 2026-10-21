#!/usr/bin/env bash
set -euo pipefail

deploy_path="${1:?usage: deploy-test.sh DEPLOY_PATH IMAGE_DIGEST}"
export MALL_IMAGE="${2:?an immutable mall image digest is required}"
[[ "$MALL_IMAGE" =~ ^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$ ]] || { echo 'An immutable image digest is required' >&2; exit 1; }
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
)
# !override in the overlay requires Compose >= 2.24.4. Fail before replacing
# any running service if configuration, credentials or the local image is absent.
"${compose[@]}" config --quiet
if "${compose[@]}" config --services | grep -qx minio; then
  minio_image="$("${compose[@]}" config --images minio)"
  docker image inspect "$minio_image" >/dev/null
fi
# For a private GHCR package, provision read-only docker login on the server.
# Never send the CI write token to the server or rebuild MinIO here.
"${compose[@]}" pull mall
if ! "${compose[@]}" up -d --wait --wait-timeout 180; then
  "${compose[@]}" ps
  "${compose[@]}" logs --tail=100 mall minio
  exit 1
fi
# MinIO is not a CI fixture. Probe the configured test API without writing data,
# including when the operator connects to an already-running external MinIO.
# These variables must expand inside the container, not on the host.
# shellcheck disable=SC2016
if ! "${compose[@]}" exec -T mall sh -ec '
  scheme=http
  [ "$STORAGE_USE_TLS" != true ] || scheme=https
  for attempt in $(seq 1 15); do
    if wget -q -O /dev/null -T 5 "$scheme://$STORAGE_ENDPOINT/minio/health/ready"; then exit 0; fi
    sleep 2
  done
  exit 1
'; then
  echo 'Deployment failed: the configured MinIO API is not ready' >&2
  exit 1
fi
printf '%s\n' "$MALL_IMAGE" > "$release_dir/image"
# Keep successful release metadata for inspection/manual rollback. Do not prune
# images, remove volumes, or automatically roll back database migrations.
ln -sfn "$release_dir" "$deploy_path/current"
echo "Deployed $MALL_IMAGE"
