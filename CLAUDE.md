# zbplan

## Development environment

This project uses Nix. All Go commands must run inside the dev shell:

```sh
nix develop --command go test ./...
nix develop --command go build ./...
```

## Lint and format

Always run lint and formatter before committing:

```sh
nix develop --command golangci-lint fmt
nix develop --command golangci-lint run ./...
```

## Update workflow

When refreshing dependencies and tooling, run the update steps inside the Nix development shell where applicable:

```sh
nix develop --command nix flake update
nix develop --command sh -c 'go get -u ./... && go mod tidy'
nix develop --command pnpx actions-up --include-branches -y
```

Also update the package `version` in `flake.nix` to the next version after the latest Git tag. For example, if the latest tag is `v0.2.2`, set the `flake.nix` version to `0.2.3` unless the release requires a minor or major bump.

## Coding guidelines

### Mockable design

Follow the interface + unexported struct pattern (see `lib/registryutil/registryutil.go`):

- Define a public interface for any type that needs to be injected or tested
- Keep the concrete implementation as an unexported struct
- Expose a `New*` constructor that returns the interface or the concrete type

### Tests

- Write unit tests for methods that have meaningful logic to verify
- Use `curl` to capture real API responses, then embed them as fixture constants in `_test.go` files (see `lib/registryutil/images_test.go` for the pattern)
- Mock HTTP calls via a custom `http.RoundTripper`, not by hitting real endpoints in unit tests

### Integration tests

Dockerfile template build tests live in `internal/plantools/` under the `integration` build tag. They require Docker to be running and spin up a BuildKit container via testcontainers.

```sh
nix develop --command go test -tags=integration -timeout=30m -count=1 ./internal/plantools/
```

Add `-v` to get full BuildKit logs on failure:

```sh
nix develop --command go test -tags=integration -timeout=30m -count=1 -v ./internal/plantools/
```

Each template in `dockerfiles/` must have a matching fixture project under `testdata/fixtures/<name>/`. Fixture projects must satisfy the Dockerfile's expectations:

- Package manager lockfiles must include at least one real dependency so the installed package directory (e.g. `node_modules/`) is created and can be copied in multi-stage builds.
- Use `docker run -v $(pwd)/internal/plantools/testdata/fixtures/<name>:/app <image> <install-cmd>` to add dependencies and regenerate lockfiles.
- Build scripts must create every artifact that the runtime stage copies (e.g. `dist/`, `.next/standalone`).
