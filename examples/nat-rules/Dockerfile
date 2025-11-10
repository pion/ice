# SPDX-FileCopyrightText: 2025 The Pion community <https://pion.ly>
# SPDX-License-Identifier: MIT

FROM golang:1.22-bookworm AS builder
WORKDIR /src

# Copy the entire repo so local changes to pion/ice are included.
COPY . .

WORKDIR /src/examples/nat-rules
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/nat-rules-demo .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /tmp/nat-rules-demo /usr/local/bin/nat-rules-demo

ENTRYPOINT ["nat-rules-demo"]
