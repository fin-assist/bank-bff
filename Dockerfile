# syntax=docker/dockerfile:1
# Image of the service: a static binary on distroless (no shell, no package manager,
# user nonroot). This stateless BFF has no migrations.
FROM golang:1.27-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOPRIVATE=github.com/fin-assist/*
COPY go.mod go.sum ./
# Private fin-assist modules: pass a token as the build secret "netrc"
# (machine github.com login x-access-token password <token>); public ones need nothing.
RUN --mount=type=secret,id=netrc,target=/root/.netrc,required=false \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/bank-bff ./cmd/bank-bff

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bank-bff /bank-bff
USER nonroot:nonroot
EXPOSE 8080 9091
ENTRYPOINT ["/bank-bff"]
CMD ["serve"]
