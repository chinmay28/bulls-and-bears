"""Kelly leverage, and the half of it that is actually traded."""

from __future__ import annotations


def kelly_leverage(mean: float, var: float) -> float:
    """Optimal leverage ``mean / var`` for a single strategy's daily returns."""
    if var <= 0:
        raise ValueError("variance must be positive")
    return mean / var


def half_kelly(mean: float, var: float, cap: float | None = None) -> float:
    """Half the Kelly leverage, optionally capped, never negative."""
    f = max(0.0, kelly_leverage(mean, var) / 2)
    return min(f, cap) if cap is not None else f
