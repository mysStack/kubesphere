FROM --platform=$BUILDPLATFORM golang:1.24.3 AS build_context

ARG TELEMETRY_VERSION=1.0.2
ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && apt-get install -y rsync
WORKDIR /workspace
ADD . /workspace/

RUN KUBE_BUILD_PLATFORMS="${TARGETOS}/${TARGETARCH}" make ks-controller-manager && \
    mkdir -p /workspace/_output/bin && \
    target="/workspace/_output/local/bin/${TARGETOS}/${TARGETARCH}/ks-controller-manager" && \
    if [ "$target" -ef /workspace/_output/bin/ks-controller-manager ]; then :; else cp "$target" /workspace/_output/bin/ks-controller-manager; fi
# RUN curl -LO https://github.com/kubesphere/telemetry/releases/download/v${TELEMETRY_VERSION}/telemetry_${TELEMETRY_VERSION}_linux_${TARGETARCH}.tar.gz && \
#     tar -zxvf telemetry_${TELEMETRY_VERSION}_linux_${TARGETARCH}.tar.gz -C /workspace/_output/bin telemetry

FROM alpine:3.21.3

COPY --from=build_context /workspace/_output/bin/ks-controller-manager /usr/local/bin/
# COPY --from=build_context /workspace/_output/bin/telemetry /usr/local/bin/
COPY config/ks-core /var/helm-charts/ks-core

WORKDIR /app

EXPOSE 8443
CMD ["sh"]
