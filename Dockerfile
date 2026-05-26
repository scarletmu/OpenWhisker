# syntax=docker/dockerfile:1

# --- build stage ---
# CGO is required for github.com/mattn/go-sqlite3, so the build image keeps gcc.
FROM golang:1.24-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=1
RUN go build -trimpath -o /out/openwhisker ./cmd/openwhisker

# --- runtime stage ---
# go-sqlite3 links against glibc, so the runtime image must not be
# scratch / distroless-static. debian-slim matches the bookworm build base.
FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

# Non-root runtime user; data/ is mounted as a writable volume.
RUN useradd --system --uid 10001 --create-home openwhisker
USER openwhisker
WORKDIR /home/openwhisker

COPY --from=build /out/openwhisker /usr/local/bin/openwhisker

# Persistent state (SQLite DB, Matrix session cache, /sync since-token).
VOLUME ["/home/openwhisker/data"]

# Default to the long-running workflow host. Override args via compose `command:`.
ENTRYPOINT ["openwhisker"]
CMD ["daemon"]
