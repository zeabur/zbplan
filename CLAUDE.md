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
