FROM golang:1.26-alpine AS builder

ARG VERSION=unknown
ARG COMMIT=unknown

RUN apk add --no-cache gcc g++ musl-dev olm-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags "-s -w \
    -X main.Tag=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X 'main.BuildTime=$(date -Iseconds)'" \
    -o /mautrix-mattermost ./cmd/mautrix-mattermost

# Binary-only export stage (used by CI to extract the binary)
FROM scratch AS binary
COPY --from=builder /mautrix-mattermost /mautrix-mattermost

# Runtime image
FROM alpine:3.21 AS runtime

RUN apk add --no-cache ca-certificates olm

COPY --from=builder /mautrix-mattermost /usr/bin/mautrix-mattermost

VOLUME /data
WORKDIR /data

EXPOSE 29319

ENTRYPOINT ["/usr/bin/mautrix-mattermost"]
CMD ["-c", "/data/config.yaml"]
