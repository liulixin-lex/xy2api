#!/bin/sh
# Builds the candidate image from ./src (a `git archive` of the branch) and
# records the tag in .env. Usage: ./build.sh <short-commit> <version>
set -eu
cd "$(dirname "$0")"
commit=${1:?short commit required}
version=${2:?version required}
tag="xy2api-staging:$commit"
DOCKER_BUILDKIT=1 docker build -q -t "$tag" \
  --build-arg VERSION="$version" --build-arg COMMIT="$commit" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --build-arg GOPROXY=https://proxy.golang.org,direct --build-arg GOSUMDB=sum.golang.org \
  src
sed -i "s|^APP_IMAGE=.*|APP_IMAGE=$tag|" .env
docker image inspect -f '{{.Id}}' "$tag"
