"""Engle-Granger and Johansen cointegration, thinly wrapped."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd
from statsmodels.tsa.vector_ar.vecm import coint_johansen

from tt.stats.stationarity import ADFResult, adf


@dataclass(frozen=True)
class EngleGranger:
    """OLS of y on x with a constant, and ADF on the residual."""

    hedge_ratio: float
    intercept: float
    residual: np.ndarray
    adf: ADFResult

    def cointegrated(self, level: str = "5%") -> bool:
        """True when the residual is stationary at ``level``."""
        return self.adf.stationary(level)


def engle_granger(y: np.ndarray, x: np.ndarray) -> EngleGranger:
    """Regress ``y`` on ``x`` and test the residual for stationarity.

    The slope is the hedge ratio a pairs strategy uses: one unit of y against
    ``hedge_ratio`` units of x.
    """
    y = np.asarray(y, dtype=float)
    x = np.asarray(x, dtype=float)
    design = np.column_stack([np.ones_like(x), x])
    coef, *_ = np.linalg.lstsq(design, y, rcond=None)
    intercept, slope = float(coef[0]), float(coef[1])
    resid = y - (intercept + slope * x)
    return EngleGranger(slope, intercept, resid, adf(resid))


@dataclass(frozen=True)
class Johansen:
    """Trace statistics against their 95% critical values, and the first eigenvector."""

    trace: np.ndarray
    critical_95: np.ndarray
    eigenvector: np.ndarray

    @property
    def rank(self) -> int:
        """Number of cointegrating relations at 95%."""
        return int(np.sum(self.trace > self.critical_95))


def johansen(df: pd.DataFrame, det_order: int = 0, k_ar_diff: int = 1) -> Johansen:
    """Johansen test on the columns of ``df`` (prices, not returns)."""
    r = coint_johansen(df.to_numpy(dtype=float), det_order, k_ar_diff)
    return Johansen(np.asarray(r.lr1), np.asarray(r.cvt[:, 1]), np.asarray(r.evec[:, 0]))
