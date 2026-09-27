# flightbay-desk image: the desk plus the tools engineers use against a
# snapshot (kubectl, k9s, istioctl, stern, jq, yq). The same image runs a
# local workspace (flightbay-desk open) or the hosted desk (flightbay-desk serve).
#
#   docker build -t flightbay-desk .
#   docker run --rm -p 8484:8484 -v $PWD:/bundles flightbay-desk open /bundles/b.tar.zst --listen 0.0.0.0:8484
#
# Both binaries are pure Go (CGO_ENABLED=0), so they run on a minimal base.

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ ./cmd/flightbay ./cmd/flightbay-desk

FROM debian:bookworm-slim AS tools
ARG TARGETARCH
ARG KUBECTL=v1.35.0 K9S=v0.50.18 ISTIOCTL=1.30.4 STERN=1.33.0 YQ=v4.47.2
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /tools
# kubectl is checked against the published SHA-256; the others come over TLS from their release pages.
RUN curl -fsSLo kubectl https://dl.k8s.io/release/${KUBECTL}/bin/linux/${TARGETARCH}/kubectl \
 && echo "$(curl -fsSL https://dl.k8s.io/release/${KUBECTL}/bin/linux/${TARGETARCH}/kubectl.sha256)  kubectl" | sha256sum -c - \
 && chmod +x kubectl
RUN curl -fsSL https://github.com/derailed/k9s/releases/download/${K9S}/k9s_Linux_${TARGETARCH}.tar.gz | tar -xz k9s
RUN curl -fsSL https://github.com/istio/istio/releases/download/${ISTIOCTL}/istioctl-${ISTIOCTL}-linux-${TARGETARCH}.tar.gz | tar -xz istioctl
RUN curl -fsSL https://github.com/stern/stern/releases/download/v${STERN}/stern_${STERN}_linux_${TARGETARCH}.tar.gz | tar -xz stern
RUN curl -fsSLo yq https://github.com/mikefarah/yq/releases/download/${YQ}/yq_linux_${TARGETARCH} && chmod +x yq

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates bash jq less zstd tar \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 10001 flightbay
COPY --from=tools /tools/ /usr/local/bin/
COPY --from=build /out/ /usr/local/bin/
USER 10001
ENV FLIGHTBAY_HOME=/home/flightbay/.flightbay
WORKDIR /home/flightbay
EXPOSE 8484 8080
ENTRYPOINT ["flightbay-desk"]
CMD ["--help"]
