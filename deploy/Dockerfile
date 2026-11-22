# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=build_ca_bundle \
    if [ -s /run/secrets/build_ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/build_ca_bundle; fi \
    && go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY schemas ./schemas
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/noema ./cmd/noema

FROM denoland/deno:bin-2.9.8 AS deno

FROM debian:bookworm-slim
COPY --from=deno /deno /usr/local/bin/deno
COPY deploy/extractors.requirements.txt /tmp/extractors.requirements.txt
RUN --mount=type=secret,id=build_ca_bundle \
    if [ -s /run/secrets/build_ca_bundle ]; then export PIP_CERT=/run/secrets/build_ca_bundle; fi \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/noema-extractors \
    && /opt/noema-extractors/bin/pip --disable-pip-version-check --no-cache-dir install -r /tmp/extractors.requirements.txt \
    && rm /tmp/extractors.requirements.txt
ENV PATH="/opt/noema-extractors/bin:${PATH}"
COPY --from=build /out/noema /usr/local/bin/noema
USER 65532:65532
ENTRYPOINT ["noema"]
CMD ["version"]
