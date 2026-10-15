# Development environments

Run commands from the repository root:

```sh
make lab
make lab-down
```

`compose.yaml` runs Teleport with the mock IdP. `compose.real.yaml` uses your
configured Zitadel endpoint. Both bind localhost port 3080 and use separate
cluster volumes. `setup.py` creates local certificates and initial resources.
Ignored `generated/` and `generated-real/` hold credentials and seed files.

See [development](../docs/development.md) for prerequisites, real-endpoint
setup, tests, and diagnostics.
