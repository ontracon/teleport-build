# Integration checks

Run against the appropriate local environment from the repository root:

```sh
make test-deps
make test-browser
make test-connectors
make test-cli
```

These commands exercise browser login, native connector management, and `tsh`
access against the mock lab. `make test-real` checks a real endpoint's login
handoff without entering account credentials.

Playwright dependencies and `test-results/` are gitignored. See
[development](../../docs/development.md#integration-tests) for browser setup,
coverage, and environment switching.
