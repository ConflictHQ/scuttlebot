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

# In a container, bind to all interfaces so port mapping works.
# Override with SCUTTLEBOT_API_ADDR / SCUTTLEBOT_MCP_ADDR if needed.
ENV SCUTTLEBOT_API_ADDR=:8080
ENV SCUTTLEBOT_MCP_ADDR=:8081

EXPOSE 8080
EXPOSE 8081

ENTRYPOINT ["scuttlebot"]
