# Keep this Go version the same as the go line in go.mod; the release job
# stops when they differ, so change both together.
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/nobs .

FROM node:22-trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git ripgrep \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g obsidian-headless@0.0.14
COPY --from=build /out/nobs /usr/local/bin/nobs
ENV OBSIDIAN_VAULT_DIR=/vault
ENV XDG_CONFIG_HOME=/config/xdg
ENTRYPOINT ["/usr/local/bin/nobs", "service"]
