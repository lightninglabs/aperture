FROM golang:1.23.6-alpine as builder

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

# Pass a tag, branch or a commit using build-arg. This allows a docker image to
# be built from a specified Git state. The default image will use the Git tip of
# master by default.
ARG checkout="master"

# Install dependencies and install/build aperture
RUN apk add --no-cache --update alpine-sdk \
    git \
    make \
    && git clone https://github.com/lightninglabs/aperture /go/src/github.com/lightninglabs/aperture \
    && cd /go/src/github.com/lightninglabs/aperture \
    && git checkout $checkout \
    && make install

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
