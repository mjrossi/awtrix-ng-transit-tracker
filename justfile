# Common dev commands. Run `just` to list them.
# `just` is pinned in mise.toml.

set shell := ["bash", "-euo", "pipefail", "-c"]

image := "urbanist-timetable"

[private]
default:
    @just --list --unsorted

# run against ./config.toml with text logs
[group('dev')]
run config="config.toml":
    go run ./cmd/urbanist-timetable --config {{config}} --log-format=text clock run

# fetch once and print each board without pushing
[group('dev')]
preview config="config.toml":
    go run ./cmd/urbanist-timetable --config {{config}} --log-format=text clock preview

# build the binary to bin/urbanist-timetable
[group('dev')]
build:
    mkdir -p bin && go build -o bin/urbanist-timetable ./cmd/urbanist-timetable

# go test with the race detector, no cache (matches CI)
[group('verify')]
test:
    go test -race -count=1 ./...

# golangci-lint (also checks formatting)
[group('verify')]
lint:
    golangci-lint run

# apply gofumpt + goimports
[group('verify')]
fmt:
    golangci-lint fmt

# regenerate render golden files after an intended layout change
[group('verify')]
golden:
    go test ./internal/render -update

# check every catalog system against its live feeds
[group('verify')]
catalog-check:
    go test -count=1 -v ./pkg/transit/catalog -run TestLive -live -timeout 10m

# build the example importer, a module of its own, from public packages only
[group('verify')]
example:
    cd examples/departures && go vet ./... && go build -o /dev/null .

# everything CI runs
[group('verify')]
ci: lint test example
    go run ./cmd/urbanist-timetable --config config.example.toml config validate

# build the Docker image
[group('docker')]
docker-build:
    docker build -t {{image}} .

# run the Docker image against ./config.toml, passing through its secrets
[group('docker')]
docker-run config="config.toml":
    @# Docker creates a directory at a missing bind-mount source.
    @test -f {{config}} || { echo "{{config}} not found: cp config.example.toml {{config}}" >&2; exit 1; }
    docker run --rm -v "$(realpath {{config}}):/config/config.toml:ro" \
        -e AWTRIX_PASSWORD -e TRANSITLAND_API_KEY -e METRO_LISBOA_CLIENT_ID -e METRO_LISBOA_CLIENT_SECRET \
        {{image}}
