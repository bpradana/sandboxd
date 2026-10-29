# Contributing to sandboxd

Thanks for your interest in improving sandboxd. Bug reports, documentation fixes, and
code are all welcome.

## Before you start

- **Bugs:** open an issue using the bug template, with steps to reproduce.
- **Security issues:** never open a public issue. Follow [SECURITY.md](SECURITY.md).
- **Larger changes:** open an issue first to agree on the approach, especially for
  anything touching isolation (`internal/engine`), the API shape, or configuration.

## Development setup

sandboxd runs only on Linux, but development works the same on macOS and Linux: every Go
command runs inside a privileged Docker container. You need
[mise](https://mise.jdx.dev) and Docker (Docker Desktop, OrbStack, or Docker Engine).

```sh
mise run tools          # host tools and the dev image
mise run docker image   # import the Alpine base image
mise run dev up         # sandboxd on http://localhost:8080 with hot reload
```

## Making a change

1. Fork the repository and create a branch from `main`.
2. Make your change, following the conventions below.
3. Add or update tests:
   - unit tests next to the code (`mise run test`)
   - end-to-end tests in `test/` for anything that touches the kernel engine,
     isolation, or API behavior (`mise run docker e2e`)
4. Run the checks:

   ```sh
   mise run check          # format check, vet, unit tests
   mise run lint --ci      # golangci-lint for linux and darwin
   mise run docker e2e     # end-to-end suite against the real kernel
   ```

5. Update documentation: the README, `.env.dist` for new settings, and the OpenAPI spec
   (`mise run docs`) for API changes. Add a line to [CHANGELOG.md](CHANGELOG.md) under
   **Unreleased** for user-visible changes.
6. Open a pull request and fill in the template.

## Conventions

- **Layering:** handler → service → repository/engine, wired with Uber Fx. One HTTP
  handler file per endpoint, with swag annotations. Services sit behind interfaces with
  gomock mocks (`mise run generate all`).
- **Platform split:** Linux-only code carries `//go:build linux`, with `_other.go` stubs
  so the project builds and lints on macOS.
- **Errors:** API errors use the codes in `internal/appctx/error_code.go`. Add new codes
  there rather than inventing strings at call sites.
- **Security-sensitive code:** explain *why* in comments (threat, invariant, kernel
  behavior). Changes that relax isolation need an e2e test proving what is still denied.
- **Commits:** small and focused, with messages that explain the reason for the change.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
