# Bootstrap Go multiarch. Extratores e FFmpeg entrarão no incremento funcional.
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/noema ./cmd/noema

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/noema /usr/local/bin/noema
USER 65532:65532
ENTRYPOINT ["noema"]
CMD ["version"]
