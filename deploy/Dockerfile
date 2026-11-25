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

FROM debian:bookworm-slim AS whisper
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates cmake curl g++ make \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN --mount=type=secret,id=build_ca_bundle \
    if [ -s /run/secrets/build_ca_bundle ]; then export CURL_CA_BUNDLE=/run/secrets/build_ca_bundle; fi \
    && curl -fsSL --retry 3 https://codeload.github.com/ggml-org/whisper.cpp/tar.gz/refs/tags/v1.9.5 -o whisper.tar.gz \
    && echo 'ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720  whisper.tar.gz' | sha256sum -c - \
    && tar xzf whisper.tar.gz --strip-components=1 \
    && cmake -S . -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF \
        -DGGML_OPENMP=OFF -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF \
    && cmake --build build --target whisper-cli -j 2
RUN --mount=type=secret,id=build_ca_bundle \
    if [ -s /run/secrets/build_ca_bundle ]; then export CURL_CA_BUNDLE=/run/secrets/build_ca_bundle; fi \
    && curl -fsSL --retry 3 'https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny-q5_1.bin?download=true' -o ggml-tiny-q5_1.bin \
    && echo '818710568da3ca15689e31a743197b520007872ff9576237bda97bd1b469c3d7  ggml-tiny-q5_1.bin' | sha256sum -c -

FROM debian:bookworm-slim
COPY --from=deno /deno /usr/local/bin/deno
COPY deploy/extractors.requirements.txt /tmp/extractors.requirements.txt
RUN --mount=type=secret,id=build_ca_bundle \
    if [ -s /run/secrets/build_ca_bundle ]; then export PIP_CERT=/run/secrets/build_ca_bundle; fi \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg python3-venv tesseract-ocr tesseract-ocr-por \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/noema-extractors \
    && /opt/noema-extractors/bin/pip --disable-pip-version-check --no-cache-dir install -r /tmp/extractors.requirements.txt \
    && rm /tmp/extractors.requirements.txt
ENV PATH="/opt/noema-extractors/bin:${PATH}"
COPY --from=whisper /src/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=whisper /src/ggml-tiny-q5_1.bin /opt/noema-models/ggml-tiny-q5_1.bin
COPY --from=build /out/noema /usr/local/bin/noema
USER 65532:65532
ENTRYPOINT ["noema"]
CMD ["version"]
