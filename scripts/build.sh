#!/bin/sh
# Builds the plugin for every platform the manifest declares.
set -eu
unset CDPATH

root=$(dirname -- "$0")
root=$(cd -- "${root}/.." && pwd)
cd "${root}"

# -buildvcs=false keeps the binaries independent of the git state, which they
# would otherwise embed: without it every commit rewrites all four files.
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%%/*}
  arch=${target##*/}
  printf 'building bin/hcc-%s-%s\n' "${os}" "${arch}"
  CGO_ENABLED=0 GOOS=${os} GOARCH=${arch} \
    go build -trimpath -buildvcs=false -ldflags '-s -w' -o "bin/hcc-${os}-${arch}" .
done
