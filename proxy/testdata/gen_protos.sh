#!/bin/sh

set -e

protoc -I/usr/local/include -I. \
       --go_out=plugins=grpc,paths=source_relative:. \
       hello.proto
