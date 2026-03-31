FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /scuttlebot ./cmd/scuttlebot

# ----

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /scuttlebot /usr/local/bin/scuttlebot

EXPOSE 8080

ENTRYPOINT ["scuttlebot"]
