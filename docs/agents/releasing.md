# Releasing tycswap

A merge releases; nobody names a version, tags, drafts or publishes a release
by hand (DESIGN A119, A121). The Release and Retry workflows run on the
workflow's own token and need no person between the merge and the published
release.

## The pull request writes its entry

- A pull request that changes what users get writes one entry at the top of
  `## Since v0.24.1` in `CHANGELOG.md`, a `- ` line (with indented lines if
  it needs more), and nothing else: no heading, no version. Start it with
  `**Fixed:**` when it fixes something. A CI-only or test-only pull request
  writes none, and its merge releases nothing.
- Its merge releases every entry the newest tag lacks (normally just its
  own) as the release's notes. The version is the newest tag with the patch
  number raised when every one of those entries starts with `**Fixed:**`,
  else the minor number; a major version is never computed.
- `git fetch origin --tags && make release-check` prints what the merge will
  release, or nothing; CI's `test` job runs the same check against the pull
  request's base.
- The check refuses a change to an entry main already has, to the sections
  below `## Since v0.24.1`, an entry anywhere else, and a version heading or
  `## Unreleased`; each message says what to move where. Correct an entry
  with a new one.
- A branch from before A121 that still writes under `## Unreleased`: merge
  main, put the entry at the top of `## Since v0.24.1`, and delete
  `## Unreleased` and any version heading it added.

## After the merge

- When CI or macOS completes on the merge commit, the Release workflow tags
  it (`tycswap vX.Y.Z`, annotated) once `test` and `windows` passed there and
  `macos` passed, has not started or found no runner, then publishes the
  release with its Linux and Windows binaries, `SHA256SUMS` and the notes.
  The macOS binaries are attached when a Mac has built them, and
  `SHA256SUMS` grows by their lines. A `macos` job that is running is waited
  for; its completion decides.
- When merges are released out of order, the later merge's release carries
  the earlier one's entries, and the earlier merge's run does nothing.
- A job that found no runner (cancelled before its first step) is rerun by
  the Retry workflow; there is nothing to do.
- A job that ran and failed holds the release: the Release run fails and
  names it. Rerun the failed job if it was a flake (its completion decides
  again), or fix it in a new pull request, whose release then carries both
  entries.
- A Release run that failed half-way is finished by **Re-run all jobs** on
  that run: nothing is uploaded twice, the Windows binary and its hash stay,
  and the notes of a published release are left as they are.
- Past GitHub's rerun bound (50 per run, 30 days):
  `gh workflow run release.yml -f tag=vX.Y.Z` builds what the release lacks
  and publishes it.

## Out of band

A tag pushed by hand still builds a draft, as before:
`git tag -a vX.Y.Z -m "tycswap vX.Y.Z" <commit> && git push origin vX.Y.Z`.
The draft's notes are the entries the tag's commit adds over the tag before
it, else a placeholder; publish it with `gh release edit vX.Y.Z
--draft=false`, or with the dispatch above.

Holding a release back is not part of the normal path; revert in the next
pull request instead. If a merge must ever wait, disable the Release
workflow (`gh workflow disable release.yml`) before its CI completes; once it
is enabled again, the next merge's release carries every entry no tag has.
