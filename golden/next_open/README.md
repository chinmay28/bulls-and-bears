# golden/next_open

Parity fixture for the `next_open` fill (docs/CONTRACTS.md, Execution):
two hand-built symbols with a gap up, a gap down, a 2-for-1 split in A's raw
prices, target flips, a repeated target and a final signal that is never
filled. `weights.csv` is the input, `equity_curve.csv` the Python engine's
output at 1 USD commission and 10 bps slippage; the Go engine must match it
to 1e-6.

Written by research/scripts/golden_next_open.py on 2026-09-08.
