FROM golang:1.15.6-buster

RUN apt-get update && apt-get install -y \
  git \
  protobuf-compiler='3.6*' \
  clang-format='1:7.0*'

ARG PROTOC_GEN_VERSION

RUN cd /tmp \
  && export GO111MODULE=on \
  && go get github.com/golang/protobuf/protoc-gen-go@${PROTOC_GEN_VERSION}

WORKDIR /build

CMD ["/bin/bash", "/build/testdata/gen_protos.sh"]
