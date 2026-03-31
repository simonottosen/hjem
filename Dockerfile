FROM node:22-alpine AS frontend
WORKDIR /build/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.24-alpine AS builder
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
ENTRYPOINT ["./hjem"]
CMD ["-db-file", "/data/hjem.db"]
