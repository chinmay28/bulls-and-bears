"""Half-life of mean reversion from an AR(1) fit (Chan, Example 7.2)."""

from __future__ import annotations

import math

import numpy as np


def halflife_ar1(series: np.ndarray) -> float:
    """Regress the change on the lagged level; half-life is ``-ln 2 / lambda``.

    Returns ``inf`` for a series that does not revert (``lambda >= 0``).
    """
    s = np.asarray(series, dtype=float)
    lagged = s[:-1]
    delta = np.diff(s)
    design = np.column_stack([np.ones_like(lagged), lagged])
    coef, *_ = np.linalg.lstsq(design, delta, rcond=None)
    lam = float(coef[1])
    # A slope indistinguishable from zero is no reversion at all, not a
    # half-life of a few hundred million bars.
    if lam >= -1e-12:
        return math.inf
    return -math.log(2) / lam
