# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------
# STAGE 1: build
# We use the official golang image ONLY to compile — it contains the
# full Go toolchain (hundreds of MB), which we do NOT want in our final
# runtime image. This is called a "multi-stage build": Docker lets a
# Dockerfile define multiple FROM stages, and later stages can COPY
# specific files out of earlier ones while discarding everything else.
# ---------------------------------------------------------------------
FROM golang:1.23-bookworm AS build

WORKDIR /src

# Copy go.mod/go.sum FIRST, before the rest of the source. Docker caches
# each layer based on whether its inputs changed — since dependencies
# change far less often than application code, this ordering means
# `go mod download` only re-runs when go.mod/go.sum actually change,
# not on every single code edit. This is one of the most common and
# highest-value Docker build optimizations.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a STATICALLY linked binary — no dependency on
# the C standard library (glibc) being present in the final image. This
# is what lets us use `scratch` (a literally EMPTY base image) below.
# GOOS/GOARCH are set explicitly rather than left to Docker's automatic
# multi-platform detection, since Docker Desktop on Apple Silicon
# defaults to building arm64 images, and we want that to be an explicit,
# readable decision in the Dockerfile, not implicit host-dependent
# behavior — if a teammate on an Intel Mac builds this image, we want
# consistent behavior, achieved via `docker buildx build --platform`
# rather than silently picking whatever the host CPU happens to be.
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

# ---------------------------------------------------------------------
# STAGE 2: runtime
# distroless/static is Google's minimal base image containing just
# CA certificates and a few essential files — no shell, no package
# manager, no OS utilities at all. This dramatically shrinks the attack
# surface: even if an attacker found a way to execute arbitrary code in
# this container, there's no shell to drop into, no package manager to
# fetch tools with. This matters more than it might seem for a service
# that (even read-only) authenticates to a Kubernetes API server.
# ---------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/server /server

# Distroless images run as a non-root user by default (uid 65532,
# "nonroot") — another defense-in-depth measure. We don't override it.
EXPOSE 8080

ENTRYPOINT ["/server"]
