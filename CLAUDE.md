# Working in this repository

Read `docs/PLAN.md` first: it is the spec, and its principles in §1 decide
anything this file does not. When the plan and reality disagree, record the
discrepancy in `docs/DISCOVERY.md` and adapt the component, not the
architecture.

## The version line follows the branch

The version is `vYEAR.MONTH.PATCH`: `Year` and `Month` are constants in
`server/internal/version/version.go`, and the patch number is the commit count.
The line is the month a branch opened in, so a release says when its work began.

**Before the first commit on any branch, run `make bump-version`** (or
`node scripts/version.mjs --bump`). It sets `Year` and `Month` to the current
month in UTC and prints whether anything moved; commit the change with the rest
of the work. Do this without being asked, and do not bump again later on the
same branch — the line is when the work started, not when it finished. Never set
the constants from the build clock at build time: a rebuild of an old tree must
still report what it originally shipped.

`make version` prints the version this tree would build as.

## Components are small and tested

Every Go package under `server/internal` owns one thing, says so in its package
comment, and carries table-driven tests. Prefer a 40-line package with 200
lines of tests. The strategy is a pure function; the risk gate runs identically
in dry-run and live; anything missing, stale or unreadable halts rather than
guesses. The LLM is a build tool here, never part of the trading decision.
