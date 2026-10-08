FROM node:22-alpine AS frontend
WORKDIR /build/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY app/ app/
COPY --from=frontend /build/frontend/dist/ frontend/dist/
COPY --from=frontend /build/frontend/index.html frontend/index.html
WORKDIR /build/app
RUN CGO_ENABLED=1 go build -ldflags "-s -w" -o hjem main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
RUN mkdir -p /app /data
WORKDIR /app
COPY --from=builder /build/app/hjem /app/

EXPOSE 8080

# /api/health answers out of in-memory counters and touches neither the database
# nor an upstream, so a 200 proves the listener is answering, not that
# Datafordeleren or Boliga are reachable. Reporting unhealthy on an upstream
# outage was rejected: a restart cannot fix someone else's outage, and it would
# pull an instance that still serves cached sales out of rotation.
# The binary is one static server with the frontend embedded, so it listens
# within a second — the start period is slack for a slow host, not a slow boot.
# A lookup can run for minutes, but in its own goroutine, never blocking this
# handler, so a short timeout stays safe on a busy server. Three strikes surface
# a wedged process in ~90s without one dropped probe flapping it.
# busybox wget ships with alpine; installing curl for this would be waste.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["./hjem"]
CMD ["-db-file", "/data/hjem.db"]
