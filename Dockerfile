# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/deckshare ./cmd/deckshare

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/deckshare /deckshare
EXPOSE 3000
ENTRYPOINT ["/deckshare"]
