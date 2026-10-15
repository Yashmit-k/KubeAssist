# syntax=docker/dockerfile:1
# Multi-stage build: compile with the full Go toolchain, ship only the
# static binary on distroless (no shell, no package manager, non-root).
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/Yashmit-k/KubeAssist/backend-go/internal/api.Version=${VERSION}" \
    -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
