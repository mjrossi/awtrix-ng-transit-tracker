# syntax=docker/dockerfile:1.7
#
# Stage 1 builds a static binary with the Go version pinned in mise.toml.
# Stage 2 is a minimal non-root Alpine runtime. Time zone data is embedded
# in the binary (time/tzdata), so the image needs no tzdata package.

FROM golang:1.27.3-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/
# Unset, the binary reports the version in cmd/urbanist-timetable/main.go.
ARG VERSION
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w${VERSION:+ -X main.version=$VERSION}" \
    -o /out/urbanist-timetable ./cmd/urbanist-timetable

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S app -G app && \
    mkdir /data && chown app:app /data
COPY --from=builder /out/urbanist-timetable /usr/local/bin/urbanist-timetable
USER app
ENV TIMETABLE_CONFIG=/config/config.toml TIMETABLE_STATE=/data/state.json TIMETABLE_CACHE=/data/cache
VOLUME ["/config", "/data"]
ENTRYPOINT ["urbanist-timetable"]
CMD ["clock", "run"]
