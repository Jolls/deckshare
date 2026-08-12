# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/enshu ./cmd/enshu

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/enshu /enshu
EXPOSE 3000
ENTRYPOINT ["/enshu"]
