# Alpine-based bridge image for the demo.
# Uses alpine (not scratch) so we can source env vars from the init container.
# Based on the project root Dockerfile with the same CGO/libolm build.
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc g++ musl-dev cmake make git

# Build libolm as a static library
RUN git clone --depth 1 --branch 3.2.16 https://gitlab.matrix.org/matrix-org/olm.git /tmp/olm && \
    cd /tmp/olm && \
    cmake -B build -DBUILD_SHARED_LIBS=OFF -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_POLICY_VERSION_MINIMUM=3.5 && \
    cmake --build build --parallel && \
    cmake --install build && \
    rm -rf /tmp/olm

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY pkg/ pkg/

RUN go build -ldflags "-s -w -linkmode external -extldflags '-static'" \
    -o /mautrix-mattermost ./cmd/mautrix-mattermost

# Runtime: alpine (not scratch) so we have a shell to source env vars
FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /mautrix-mattermost /usr/bin/mautrix-mattermost
COPY example/multi-agent-puppeting/bridge/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME /data
WORKDIR /data

EXPOSE 29319 29320

ENTRYPOINT ["/entrypoint.sh"]
