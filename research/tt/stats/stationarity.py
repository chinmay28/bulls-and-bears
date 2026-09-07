"""Augmented Dickey-Fuller, with a return type instead of a tuple."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
from statsmodels.tsa.stattools import adfuller


@dataclass(frozen=True)
class ADFResult:
    """The test statistic, its p-value and the 1/5/10% critical values."""

    statistic: float
    pvalue: float
    critical: dict[str, float]
    lags: int

    def stationary(self, level: str = "5%") -> bool:
        """True when the statistic is below the critical value at ``level``."""
        return self.statistic < self.critical[level]


def adf(series: np.ndarray, regression: str = "c") -> ADFResult:
    """Run ADF on a series (constant, no trend by default)."""
    r = adfuller(np.asarray(series, dtype=float), regression=regression, result_object=False)
    stat, p, lags, _, crit = r[0], r[1], r[2], r[3], r[4]
    return ADFResult(float(stat), float(p), {k: float(v) for k, v in crit.items()}, int(lags))
