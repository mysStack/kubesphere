# Build
FROM golang:1.23.7 AS build_context

ENV OUTDIR=/out
RUN mkdir -p ${OUTDIR}/usr/local/bin/

WORKDIR /workspace
ADD . /workspace/

RUN make ks-controller-manager
RUN mv /workspace/bin/cmd/controller-manager ${OUTDIR}/usr/local/bin/controller-manager

# Final Image
FROM alpine:3.21.3

COPY --from=build_context /out/ /
COPY config/ks-core /var/helm-charts/ks-core

WORKDIR /

EXPOSE 8443
CMD ["sh"]
