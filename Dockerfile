FROM golang:1.13-alpine as builder

# Copy in the local repository to build from.
COPY . /go/src/github.com/lightninglabs/aperture

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

# Explicitly turn on the use of modules (until this becomes the default).
ENV GO111MODULE on

# Install dependencies and install/build aperture
RUN apk add --no-cache --update alpine-sdk \
    git \
    make \
&&  cd /go/src/github.com/lightninglabs/aperture/cmd \
&&  go install ./...

# Start a new, final image to reduce size.
FROM alpine as final

# Expose aperture ports
EXPOSE 11010

# Copy the binaries and entrypoint from the builder image.
COPY --from=builder /go/bin/aperture /bin/

# Add bash.
RUN apk add --no-cache \
    bash \
    ca-certificates

# Specify the start command and entrypoint as the aperture daemon.
ENTRYPOINT ["aperture"]
