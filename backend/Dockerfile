# Four binaries come out of this build: moderator, discovery, persona, and
# the one-shot seeder. They share a module and a build cache, so building
# them together is cheaper than four images, and each compose/k8s service
# just picks its own binary via `command`.
FROM golang:1.26 AS build

WORKDIR /src

# Copy the manifests alone first so `go mod download` stays cached until the
# dependency set actually changes, not on every source edit.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 makes every binary statically linked, which is what lets the
# runtime stage be distroless/static — no libc, no shell, nothing to patch.
RUN CGO_ENABLED=0 go build -o /out/moderator ./cmd/moderator && \
    CGO_ENABLED=0 go build -o /out/discovery ./cmd/discovery && \
    CGO_ENABLED=0 go build -o /out/persona   ./cmd/persona   && \
    CGO_ENABLED=0 go build -o /out/seed      ./cmd/seed

FROM gcr.io/distroless/static-debian12 AS runtime

# cmd/seed resolves its fixtures as the relative path testdata/knowledge, so
# the working directory has to be the one holding that tree.
WORKDIR /app

COPY --from=build /out/moderator /app/moderator
COPY --from=build /out/discovery /app/discovery
COPY --from=build /out/persona   /app/persona
COPY --from=build /out/seed      /app/seed
COPY testdata /app/testdata

# No EXPOSE / ENTRYPOINT here on purpose: which binary a container runs, and
# on which ADDR, is a per-service choice (see docker-compose.yml's
# `command:` and k8s/base/*.yaml) — none of the four is a sensible default
# for the image as a whole.
