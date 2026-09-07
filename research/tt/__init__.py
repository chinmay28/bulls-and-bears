"""Bulls and Bears research: data, statistics, backtests and what they emit.

The package owns the Python half of docs/PLAN.md — fetching and checking
bars, the statistics a strategy is chosen with, the backtest engine and its
metrics, the strategies' signal functions, and the writer that turns a result
into a spec and golden files for the Go runtime. It never talks to a broker.
"""
