## What and why

<!-- What changes for users or contributors, and why. Link the issue if there is one. -->

## Checklist

- [ ] `just ci` passes
- [ ] A `CHANGELOG.md` entry under `Unreleased`, for a user-visible change
- [ ] `just golden` run and its diff reviewed, for a layout change
- [ ] Docs and comments updated where the change makes them wrong
- [ ] A new provider passes `transittest.Run` against responses in its `testdata/` (see `CONTRIBUTING.md`)
