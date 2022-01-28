#!/bin/bash

set -e

# Directory of the script file, independent of where it's called from.
DIR="$(cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd)"
echo $DIR
PROTOC_GEN_VERSION=$(go list -f '{{.Version}}' -m github.com/golang/protobuf)

echo "Building protobuf compiler docker image..."
docker build -q -t nautilus-protobuf-builder \
  --build-arg PROTOC_GEN_VERSION="$PROTOC_GEN_VERSION" \
  .

echo "Compiling and formatting *.proto files..."
docker run \
  --rm \
  --user $UID:$UID \
  -e UID=$UID \
  -v "$DIR/../:/build" \
  nautilus-protobuf-builder
