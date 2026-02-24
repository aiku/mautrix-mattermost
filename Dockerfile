FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc g++ musl-dev olm-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -ldflags "-s -w" -o /mautrix-mattermost ./cmd/mautrix-mattermost

FROM alpine:3.20

RUN apk add --no-cache ca-certificates olm

COPY --from=builder /mautrix-mattermost /usr/bin/mautrix-mattermost

VOLUME /data
WORKDIR /data

EXPOSE 29319

ENTRYPOINT ["/usr/bin/mautrix-mattermost"]
CMD ["-c", "/data/config.yaml"]
