# Strategy template

Fill one of these in before a strategy gets a spec. If a section cannot be
written, the strategy is not ready to be backtested, never mind traded.

## Name

`snake_case`, the same as the spec's `name` and the golden directory.

## Hypothesis

One paragraph. Why should this make money, and who is on the other side?
Borrowed from where? (Chan, *Quantitative Trading*, example number if any.)

## Universe

The symbols, and why these. Note survivorship bias: Yahoo data forgets
delisted tickers, which is acceptable for ETFs and must be flagged for a stock
basket.

## Signal

The pure function, in words: inputs (bars, positions), output (target
weights). No I/O, no clock, no randomness.

## Sizing

Kelly estimate from the training window, and the half-Kelly leverage actually
used. Per-leg notional cap.

## Exit

The exit signal, and the hard time stop.

## Invalidation

What result would mean the hypothesis is wrong: the OOS Sharpe floor (1.0),
the drawdown you will not sit through, the paper-vs-backtest gap that means
the backtest lied.

## Result

Filled in by research: train and test windows, OOS Sharpe, max drawdown, cost
model, the git SHA of the research that produced the spec.
