#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
# These fixtures migrate PostgreSQL, FlushDB Redis DB 15 and write S3 objects.
# Callers must explicitly acknowledge exclusive, disposable infrastructure.
: "${ECOMMERCE_INTEGRATION_EXCLUSIVE:?dedicated disposable dependencies must be acknowledged}"
[[ "$ECOMMERCE_INTEGRATION_EXCLUSIVE" == true ]] || { echo 'exclusive integration dependencies required' >&2; exit 1; }
for name in ECOMMERCE_INTEGRATION_POSTGRES_URL ECOMMERCE_INTEGRATION_REDIS_ADDR ECOMMERCE_INTEGRATION_S3_ENDPOINT ECOMMERCE_INTEGRATION_S3_BUCKET ECOMMERCE_INTEGRATION_S3_ACCESS_KEY ECOMMERCE_INTEGRATION_S3_SECRET_KEY; do
  [[ -n "${!name:-}" ]] || { echo "required integration variable missing: $name" >&2; exit 1; }
done
# Tests additionally parse and guard the database name; never print credentials.
[[ "${ECOMMERCE_INTEGRATION_S3_BUCKET,,}" == *integration* ]] || { echo 'integration bucket name required' >&2; exit 1; }
go test -race -tags=integration -count=1 -timeout=5m -p=1 ./app/mall/internal/data/... ./app/mall/internal/job/...
