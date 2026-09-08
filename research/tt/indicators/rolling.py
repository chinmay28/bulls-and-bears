"""Rolling mean, sample standard deviation, z-score and return."""

from __future__ import annotations

import numpy as np
import pandas as pd


def _window(lookback: int) -> None:
    if lookback < 2:
        raise ValueError("lookback must be >= 2")


def rolling_mean(x: np.ndarray, lookback: int) -> np.ndarray:
    """Mean over the window of ``lookback`` ending at t; NaN for t < lookback − 1."""
    _window(lookback)
    return np.asarray(pd.Series(np.asarray(x, dtype=float)).rolling(lookback).mean().to_numpy())


def rolling_sample_sd(x: np.ndarray, lookback: int) -> np.ndarray:
    """Sample sd (ddof = 1) over the window of ``lookback`` ending at t."""
    _window(lookback)
    return np.asarray(pd.Series(np.asarray(x, dtype=float)).rolling(lookback).std(ddof=1).to_numpy())


def rolling_zscore(x: np.ndarray, lookback: int) -> np.ndarray:
    """``(x_t − mean_t) / sd_t``; NaN while the window is short or the sd is 0."""
    xs = np.asarray(x, dtype=float)
    mean = rolling_mean(xs, lookback)
    sd = rolling_sample_sd(xs, lookback)
    with np.errstate(divide="ignore", invalid="ignore"):
        z = (xs - mean) / sd
    z[~(sd > 0)] = np.nan
    return np.asarray(z)


def rolling_return(px: np.ndarray, lookback: int) -> np.ndarray:
    """``p_t / p_{t−L} − 1``; NaN for t < L. L ≥ 1."""
    if lookback < 1:
        raise ValueError("lookback must be >= 1")
    p = np.asarray(px, dtype=float)
    out = np.full(len(p), np.nan)
    if len(p) > lookback:
        out[lookback:] = p[lookback:] / p[:-lookback] - 1
    return out
