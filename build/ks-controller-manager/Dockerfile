# Download dependencies
FROM alpine:3.21.3 AS base_os_context

RUN apk add --no-cache ca-certificates curl

ENV OUTDIR=/out
RUN mkdir -p ${OUTDIR}/usr/local/bin/

# The upstream telemetry binary release is no longer available. Telemetry is
# optional at runtime, so omit the unavailable binary instead of making every
# controller image build depend on a removed external artifact.

# Add KS Core Helm Chart
COPY config/ks-core ${OUTDIR}/var/helm-charts/ks-core

# Build
FROM golang:1.23.7 AS build_context

ENV OUTDIR=/out
RUN mkdir -p ${OUTDIR}/usr/local/bin/

WORKDIR /workspace
ADD . /workspace/

RUN make ks-controller-manager
RUN mv /workspace/bin/cmd/controller-manager ${OUTDIR}/usr/local/bin/

# Final Image
FROM alpine:3.21.3

COPY --from=base_os_context /out/ /
COPY --from=build_context /out/ /

WORKDIR /

EXPOSE 8443
CMD ["sh"]