# golden/indicators

Parity fixtures for docs/CONTRACTS.md "Indicators": `series.csv` is a
seeded synthetic OHLC series, `expected.csv` every indicator's output from
`research/tt/indicators`, `NaN` where the contract says undefined. The Go
package `internal/indicator` must reproduce each column to 1e-9.

Written by research/scripts/golden_indicators.py on 2026-09-08.
