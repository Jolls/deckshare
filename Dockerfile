# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/deckshare ./cmd/deckshare
RUN mkdir -p /out/media

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/deckshare /deckshare
# Pre-create the media dir owned by distroless's nonroot uid: a fresh named volume copies this
# ownership on first mount, so the app can write without running as root.
COPY --from=build --chown=65532:65532 /out/media /var/lib/deckshare/media
ENV ADDR=:3000 \
    MEDIA_ROOT=/var/lib/deckshare/media
VOLUME /var/lib/deckshare/media
USER 65532:65532
EXPOSE 3000
ENTRYPOINT ["/deckshare"]
