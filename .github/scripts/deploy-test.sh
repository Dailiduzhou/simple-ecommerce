#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

for name in MALL_IMAGE TEST_SSH_HOST TEST_SSH_USER TEST_SSH_PRIVATE_KEY TEST_SSH_KNOWN_HOSTS TEST_SSH_PORT TEST_DEPLOY_PATH RELEASE_ID; do
  [[ -n "${!name:-}" ]] || { echo "Required deployment variable missing: $name" >&2; exit 1; }
done
# These values become remote shell arguments. Restrict them before interpolation.
[[ "$TEST_SSH_HOST" =~ ^[a-zA-Z0-9][a-zA-Z0-9.:-]*$ ]] || { echo 'Invalid SSH host' >&2; exit 1; }
[[ "$TEST_SSH_USER" =~ ^[a-zA-Z_][a-zA-Z0-9_-]*$ ]] || { echo 'Invalid SSH user' >&2; exit 1; }
if [[ ! "$TEST_SSH_PORT" =~ ^[0-9]{1,5}$ ]] || ((10#$TEST_SSH_PORT < 1 || 10#$TEST_SSH_PORT > 65535)); then
  echo 'Invalid SSH port' >&2
  exit 1
fi
TEST_DEPLOY_PATH="${TEST_DEPLOY_PATH%/}"
[[ "$TEST_DEPLOY_PATH" =~ ^/[a-zA-Z0-9_/-]+$ ]] || { echo 'Use an absolute deployment path containing only letters, numbers, /, _ and -' >&2; exit 1; }
[[ "$RELEASE_ID" =~ ^[a-zA-Z0-9_-]+$ ]] || { echo 'Invalid release ID' >&2; exit 1; }
[[ "$MALL_IMAGE" =~ ^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$ ]] || { echo 'An immutable image digest is required' >&2; exit 1; }

ssh_dir="$(mktemp -d)"
trap 'rm -rf "$ssh_dir"' EXIT
chmod 700 "$ssh_dir"
printf '%s\n' "$TEST_SSH_PRIVATE_KEY" > "$ssh_dir/id"
printf '%s\n' "$TEST_SSH_KNOWN_HOSTS" > "$ssh_dir/known_hosts"
chmod 600 "$ssh_dir/id" "$ssh_dir/known_hosts"
ssh_options=(
  -i "$ssh_dir/id" -p "$TEST_SSH_PORT"
  -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$ssh_dir/known_hosts"
  -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=3
)
target="$TEST_SSH_USER@$TEST_SSH_HOST"
release_dir="$TEST_DEPLOY_PATH/releases/$RELEASE_ID"
# Remote arguments intentionally expand here, after the validation above.
# shellcheck disable=SC2029
ssh "${ssh_options[@]}" "$target" "umask 077; mkdir -p '$release_dir'"
# Only versioned deployment/configuration files travel to the server. Runtime
# secrets remain in TEST_DEPLOY_PATH/.env, outside these release directories.
# shellcheck disable=SC2029
tar -czf - docker-compose.yml docker-compose.test.yml deploy app/mall/configs |
  ssh "${ssh_options[@]}" "$target" "tar -xzf - -C '$release_dir'"
# shellcheck disable=SC2029
ssh "${ssh_options[@]}" "$target" \
  "bash '$release_dir/deploy/deploy-test.sh' '$TEST_DEPLOY_PATH' '$MALL_IMAGE'"
