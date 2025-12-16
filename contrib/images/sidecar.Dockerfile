# syntax=docker/dockerfile:1

# --------------------------------------------------------
# Arguments
# --------------------------------------------------------

ARG GO_VERSION="1.25.2"
ARG ALPINE_VERSION="3.22"
ARG VERSION="dev"
ARG COMMIT=""
ARG BUILD_TAGS="muslc"
ARG DRAND_CONN_INSECURE="false"
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
ARG DRAND_CONN_INSECURE
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

# Build sidecar + drand binaries
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    VERSION=${VERSION} COMMIT=${COMMIT} \
    LEDGER_ENABLED=${LEDGER_ENABLED} BUILD_TAGS=${BUILD_TAGS} \
    LINK_STATICALLY=${LINK_STATICALLY} SKIP_TIDY=${SKIP_TIDY} \
    make build-sidecar \
    && file /vrf/bin/sidecar \
    && echo "Ensuring sidecar binary is statically linked ..." \
    && (file /vrf/bin/sidecar | grep "statically linked") \
    && DRAND_VERSION="$(go list -m -f '{{.Version}}' github.com/drand/drand/v2)" \
    && echo "Installing drand ${DRAND_VERSION} ..." \
    && DRAND_TAGS="netgo ${BUILD_TAGS}" \
    && if [ "${DRAND_CONN_INSECURE}" = "true" ]; then DRAND_TAGS="${DRAND_TAGS} conn_insecure"; fi \
    && CGO_ENABLED=0 GOBIN=/vrf/bin go install -trimpath -tags "${DRAND_TAGS}" -ldflags "-s -w" github.com/drand/drand/v2/cmd/drand@${DRAND_VERSION} \
    && /vrf/bin/drand --version >/dev/null

# --------------------------------------------------------
# Runner
# --------------------------------------------------------

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates python3
COPY --from=builder /vrf/bin/sidecar /bin/sidecar
COPY --from=builder /vrf/bin/drand /bin/drand

ENV HOME=/.sidecar
WORKDIR $HOME

EXPOSE 8090
EXPOSE 8091
EXPOSE 8081
EXPOSE 4444
EXPOSE 8881

ENTRYPOINT ["sidecar"]
