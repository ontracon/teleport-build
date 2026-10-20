# Teleport integration

Existing upstream files are maintained as Git patches. `overlay/` follows
Teleport's source tree and contains only new files: the Auth/Proxy bridge,
plugin implementation, native connector handlers, editor/template additions,
and focused bridge/UI tests.

`build/prepare-teleport-source.sh` verifies the supported commit, preflights all
changes, applies the series, and installs new files. `build/Dockerfile` invokes
the same script. Original replacement code and copied upstream components are
preserved locally under gitignored `deprecated/`.

## Patch series

| Patch | Scope |
| --- | --- |
| `0001-register-zitadel-plugin.patch` | Register the extension at service startup |
| `0002-display-verified-email.patch` | Expose display data without changing identity |
| `0003-manage-zitadel-connectors.patch` | Native connector APIs, defaults, and UI |

`patches/series` lists the application order. `upstream.commit` records the
exact supported source commit. Changed upstream files are represented by
contextual diffs, so conflicts appear directly in Git's patch diagnostics.

## Prepare source

Use a fresh checkout of the supported commit. From this repository's root:

```sh
./build/prepare-teleport-source.sh --source /path/to/teleport --check
make prepare-source SOURCE_DIR=/path/to/teleport
```

Preflight verifies the revision, checks every patch against the index and
working tree, and rejects existing adapter/overlay destinations. It writes
nothing until all checks pass. Preparation leaves upstream modifications
unstaged and does not create commits. Running it again fails. Prepare a fresh
checkout when rebuilding an edited patch series. It never resets developer
changes or automatically resolves conflicts.

## Maintain changes

Edit new files in `overlay/` or the independent `oidc-adapter/` module. Edit
existing upstream files in a prepared checkout, then refresh the patch owning
those files. For example, to update the startup hook:

```sh
git -C /path/to/teleport diff --full-index -- lib/service/service.go \
  > integration/patches/0001-register-zitadel-plugin.patch
```

Select only that patch's files when exporting a diff. Each current patch owns
a distinct set of upstream files. Preserve upstream copyright/license headers
and avoid unrelated formatting changes. Add genuinely new upstream edits to
the appropriate patch, or add a numbered patch and list it in `series`.

Verify the refreshed series using an existing upstream checkout. The test
creates a separate sparse clone and leaves the supplied checkout untouched:

```sh
make test-patches SOURCE_DIR=/path/to/teleport
```

Then run `make lab-build` and the browser/CLI integration checks. Patch CI runs
applicability and guard checks on integration changes. A source upgrade requires
updating the pin, rebasing the series, aligning build/toolchain pins, and
repeating compilation and integration checks.

- [Architecture and supported scope](../docs/architecture.md)
- [Connector configuration](../docs/configuration.md)
- [Build workflow](../docs/building.md)
- [Local integration checks](../docs/development.md#integration-tests)

The shared build includes the integration in both container images and Linux
release archives. The plugin registers automatically. Configure a native
Zitadel connector to use it.
