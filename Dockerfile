# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/fileflow ./cmd/fileflow && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/ffbench ./cmd/ffbench

# distroless: no shell, no package manager, runs as non-root (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fileflow /out/ffbench /usr/local/bin/
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/fileflow"]
