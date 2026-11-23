# MinIO's community distribution is source-only. Build the frozen fixture from
# official source instead of pulling the withdrawn quay.io/minio/minio image.
FROM quay.io/zncdatadev/go-devel:1.25.8-kubedoop0.0.0-dev AS builder

# Server: RELEASE.2025-07-23T15-54-02Z
# Client: RELEASE.2025-07-21T05-28-08Z
ARG MINIO_COMMIT=7ced9663e6a791fef9dc6be798ff24cda9c730ac
ARG MC_COMMIT=ee72571936f15b0e65dc8b4a231a4dd445e5ccb6
ENV CGO_ENABLED=0
WORKDIR /src
RUN mkdir -p /out && \
    git init minio && \
    git -C minio remote add origin https://github.com/minio/minio.git && \
    git -C minio fetch --depth=1 origin ${MINIO_COMMIT} && \
    git -C minio checkout --detach FETCH_HEAD && \
    test "$(git -C minio rev-parse HEAD)" = "${MINIO_COMMIT}" && \
    cd minio && go build -mod=readonly -p 2 -trimpath -o /out/minio .
RUN git init mc && \
    git -C mc remote add origin https://github.com/minio/mc.git && \
    git -C mc fetch --depth=1 origin ${MC_COMMIT} && \
    git -C mc checkout --detach FETCH_HEAD && \
    test "$(git -C mc rev-parse HEAD)" = "${MC_COMMIT}" && \
    cd mc && go build -mod=readonly -p 2 -trimpath -o /out/mc .

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
LABEL org.opencontainers.image.title="Kubedoop MinIO E2E fixture" \
      org.opencontainers.image.source="https://github.com/minio/minio"
COPY --from=builder /out/minio /out/mc /usr/bin/
COPY --from=builder /src/minio/LICENSE /usr/share/licenses/minio/LICENSE
COPY --from=builder /src/mc/LICENSE /usr/share/licenses/mc/LICENSE
ENTRYPOINT ["/usr/bin/minio"]
