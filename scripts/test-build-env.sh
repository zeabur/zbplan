#!/usr/bin/env bash
set -euo pipefail

# Run from this module, or pass additional package import paths in a Go workspace.
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"
docker info >/dev/null
test_container="zeabur-build-env-${RANDOM}-$$"
cleanup() { docker rm -f "$test_container" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run --detach --privileged --name "$test_container" \
  "${BUILD_ENV_TEST_BUILDKIT_IMAGE:-moby/buildkit@sha256:2f5adac4ecd194d9f8c10b7b5d7bceb5186853db1b26e5abd3a657af0b7e26ec}" >/dev/null
ready=false
for ((attempt = 0; attempt < 100; attempt++)); do
  if docker exec "$test_container" buildctl debug workers >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.2
done
if [[ "$ready" != true ]]; then
  docker logs "$test_container" >&2
  exit 1
fi
export BUILDKIT_HOST="docker-container://$test_container"
export BUILD_ENV_TEST_BASE="${BUILD_ENV_TEST_BASE:-alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce}"
if [[ -z "${BUILD_ENV_TEST_HOST:-}" && "$(uname -s)" == Linux ]]; then
  BUILD_ENV_TEST_HOST=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
  export BUILD_ENV_TEST_HOST
fi
# A missing or unreachable daemon is never a passing check.
go test -tags=integration -count=1 -timeout=10m \
  github.com/zeabur/zbplan/pkg/buildenv github.com/zeabur/zbplan/pkg/builder "$@"
