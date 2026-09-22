FROM alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1

RUN addgroup -S -g 65532 wip-smoke \
    && adduser -S -D -H -u 65532 -G wip-smoke wip-smoke \
    && mkdir -p /var/lib/wip-smoke \
    && chown -R 65532:65532 /var/lib/wip-smoke

COPY --chmod=0555 entrypoint /usr/local/bin/wip-orb-smoke-entrypoint

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/wip-orb-smoke-entrypoint"]
