# Two binaries come out of this build: the API server and the one-shot seeder.
# They share a module and a build cache, so building them together is cheaper
# than two images, and the compose `seed` service just overrides the command.
FROM golang:1.26 AS build

WORKDIR /src

# Copy the manifests alone first so `go mod download` stays cached until the
# dependency set actually changes, not on every source edit.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 makes both binaries statically linked, which is what lets the
# runtime stage be distroless/static — no libc, no shell, nothing to patch.
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /out/seed ./cmd/seed

FROM gcr.io/distroless/static-debian12 AS runtime

# cmd/seed resolves its fixtures as the relative path testdata/knowledge, so
# the working directory has to be the one holding that tree.
WORKDIR /app

COPY --from=build /out/server /app/server
COPY --from=build /out/seed /app/seed
COPY testdata /app/testdata

EXPOSE 8080

# Exec form is mandatory: distroless has no shell to interpret a string form.
ENTRYPOINT ["/app/server"]
