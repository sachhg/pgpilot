# Build stage. cgo is required: pg_query compiles the real PostgreSQL parser from
# C, so the build needs a C toolchain.
FROM golang:1.22-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=0.1.0
RUN CGO_ENABLED=1 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /pgpilot ./cmd/pgpilot

# Runtime stage. Distroless still carries glibc, which the cgo-linked binary
# needs; nonroot runs as an unprivileged user.
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /pgpilot /usr/local/bin/pgpilot

# 6432: client listener (convention). 9090: metrics, when enabled.
EXPOSE 6432 9090
ENTRYPOINT ["pgpilot"]
CMD ["-config", "/etc/pgpilot/pgpilot.json"]
