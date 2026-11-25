# Releasing tycswap

A merge releases; nobody tags, drafts or publishes a release by hand
(DESIGN A119). The Release and Retry workflows run on the workflow's own
token and need no person between the merge and the published release.

## The pull request names the version

- Entries go under `## Unreleased` in `CHANGELOG.md`. A pull request that is
  not to be released writes only there; its merge releases nothing.
- To release, rename `## Unreleased` to `## vMAJOR.MINOR.PATCH`, greater than
  every tag, and put an empty `## Unreleased` above it. That section, as
  written, is the release's notes. One pull request names one version.
- `git fetch origin --tags && make release-check` says what the merge will
  release; CI's `test` job runs the same check against the pull request's base.
- A section main already has never changes, `## v0.23.3 and earlier` (the
  history from before versions had sections) included: correct a released
  note in the next version's section.
- When another pull request named the same version and merged first, your
  check fails once you merge main: name the next version, or move your entries
  back under `## Unreleased`.

## After the merge

- When CI completes on the merge commit, the Release workflow tags it
  (`tycswap vX.Y.Z`, annotated) if `test` and `windows` passed and `macos`
  passed or found no runner, then publishes the release with its Linux and
  Windows binaries, `SHA256SUMS` and the notes. The macOS binaries are attached
  when a Mac has built them, and `SHA256SUMS` grows by their lines.
- A job that found no runner (cancelled before its first step) is rerun by the
  Retry workflow; there is nothing to do.
- A job that ran and failed holds the release: the Release run fails and names
  it. Rerun CI's failed job if it was a flake (CI's completion decides again),
  or fix it in a new pull request that names the next version.
- A Release run that failed half-way is finished by **Re-run all jobs** on that
  run: nothing is uploaded twice, the Windows binary and its hash stay, and the
  notes of a published release are left as they are.
- Past GitHub's rerun bound (50 per run, 30 days):
  `gh workflow run release.yml -f tag=vX.Y.Z` builds what the release lacks and
  publishes it, since the tag's commit has the version's section.

## Out of band

A tag pushed by hand still builds a draft, as before:
`git tag -a vX.Y.Z -m "tycswap vX.Y.Z" <commit> && git push origin vX.Y.Z`. The
draft's notes are the tag commit's `## vX.Y.Z` section when it has one, else a
placeholder; publish it with `gh release edit vX.Y.Z --draft=false`, or with
the dispatch above when the section exists.
