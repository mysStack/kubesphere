# Build
FROM --platform=$BUILDPLATFORM golang:1.24.3 AS build_context

ARG TARGETOS
ARG TARGETARCH

RUN apt-get update && apt-get install -y rsync
WORKDIR /workspace
ADD . /workspace/

RUN KUBE_BUILD_PLATFORMS="${TARGETOS}/${TARGETARCH}" make ks-apiserver && \
    mkdir -p /workspace/_output/bin && \
    cp "/workspace/_output/local/bin/${TARGETOS}/${TARGETARCH}/ks-apiserver" \
       /workspace/_output/bin/ks-apiserver

FROM alpine:3.21.3

COPY --from=build_context /workspace/_output/bin/ks-apiserver /usr/local/bin/

WORKDIR /app

EXPOSE 9090
CMD ["sh"]
