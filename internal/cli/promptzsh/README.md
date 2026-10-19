# Flakelab terminal prompts

`prompt.zsh` is a byte-identical copy of
[`files/scripts/lib/prompt.zsh`](https://github.com/tyclab/flakelab/blob/294b864c2d603df6d1187e2405ce8accd7eaceef/files/scripts/lib/prompt.zsh)
from flakelab commit `294b864c2d603df6d1187e2405ce8accd7eaceef`.
Its MIT license is included beside it. Update the copy from upstream; keep
tycswap's adapter in `../zshprompt.go` rather than changing this library.

The Go binary embeds this library, so release binaries need no sidecar files.
When zsh is available it renders selections and line input. The existing Go
renderer supports installations without zsh. Account operations stay in Go.
