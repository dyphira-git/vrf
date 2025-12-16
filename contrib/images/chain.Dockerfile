# syntax=docker/dockerfile:1

# --------------------------------------------------------
# Arguments
# --------------------------------------------------------

ARG GO_VERSION="1.25.2"
ARG ALPINE_VERSION="3.22"
ARG VERSION="dev"
ARG COMMIT=""
ARG BUILD_TAGS="muslc"
ARG LEDGER_ENABLED="false"
ARG LINK_STATICALLY="true"
ARG SKIP_TIDY="true"

# --------------------------------------------------------
# Builder
# --------------------------------------------------------

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder
ARG GO_VERSION
ARG VERSION
ARG COMMIT
ARG BUILD_TAGS
ARG LEDGER_ENABLED
ARG LINK_STATICALLY
ARG SKIP_TIDY

ENV GOTOOLCHAIN=go${GO_VERSION}

RUN apk add --no-cache \
    ca-certificates \
    build-base \
    linux-headers \
    git

WORKDIR /vrf

# Copy Go dependencies
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

# Copy source code
COPY . .

# Build binary
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    VERSION=${VERSION} COMMIT=${COMMIT} \
    LEDGER_ENABLED=${LEDGER_ENABLED} BUILD_TAGS=${BUILD_TAGS} \
    LINK_STATICALLY=${LINK_STATICALLY} SKIP_TIDY=${SKIP_TIDY} \
    make build-chain \
    && file /vrf/bin/chaind \
    && echo "Ensuring binary is statically linked ..." \
    && (file /vrf/bin/chaind | grep "statically linked")

# --------------------------------------------------------
# Runner
# --------------------------------------------------------

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates jq
COPY --from=builder /vrf/bin/chaind /bin/chaind

ENV HOME=/.vrf
WORKDIR $HOME

EXPOSE 26656
EXPOSE 26657
EXPOSE 1317
EXPOSE 9090

ENTRYPOINT ["chaind"]
