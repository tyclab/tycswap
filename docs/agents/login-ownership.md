# Login ownership and refresh tokens

Read for this topic; paths below are relative to the repository root unless stated otherwise.

A login lives on one machine. Every OAuth refresh rotates the refresh token, so a copied
store or an exported account survives one refresh: the machine that refreshes first keeps
the login and the other shows `re-login needed — refresh token dead`. A second machine
gets its own `tycswap add --login`; export/import moves a login, it does not share it.
