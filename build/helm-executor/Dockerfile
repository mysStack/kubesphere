FROM --platform=$BUILDPLATFORM alpine:3.22 AS helm

ARG TARGETARCH
ARG HELM_VERSION=v3.22.0

RUN set -eux; \
    apk add --no-cache curl tar; \
    case "${TARGETARCH}" in amd64|arm64) ;; *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; esac; \
    curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" -o /tmp/helm.tgz; \
    tar -xzf /tmp/helm.tgz -C /tmp

FROM docker.1ms.run/kubesphere/kubectl:v1.33.4

ARG TARGETARCH

COPY --from=helm /tmp/linux-${TARGETARCH}/helm /usr/local/bin/helm
