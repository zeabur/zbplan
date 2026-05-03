# zbplan

## Development environment

This project uses Nix. All Go commands must run inside the dev shell:

```
nix develop --command go test ./...
nix develop --command go build ./...
```

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

```
nix develop --command go test -tags=integration -timeout=30m -count=1 ./internal/plantools/
```

Add `-v` to get full BuildKit logs on failure:

```
nix develop --command go test -tags=integration -timeout=30m -count=1 -v ./internal/plantools/
```

Each template in `dockerfiles/` must have a matching fixture project under `testdata/fixtures/<name>/`. Fixture projects must satisfy the Dockerfile's expectations:

- Package manager lockfiles must include at least one real dependency so the installed package directory (e.g. `node_modules/`) is created and can be copied in multi-stage builds.
- Use `docker run -v $(pwd)/internal/plantools/testdata/fixtures/<name>:/app <image> <install-cmd>` to add dependencies and regenerate lockfiles.
- Build scripts must create every artifact that the runtime stage copies (e.g. `dist/`, `.next/standalone`).
