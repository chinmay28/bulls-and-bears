import math

import numpy as np
import pandas as pd
import pytest

from tt.stats.cointegration import engle_granger, johansen
from tt.stats.halflife import halflife_ar1
from tt.stats.kelly import half_kelly, kelly_leverage
from tt.stats.stationarity import adf


def ar1(n: int, phi: float, seed: int = 0, sigma: float = 1.0) -> np.ndarray:
    rng = np.random.default_rng(seed)
    x = np.zeros(n)
    for t in range(1, n):
        x[t] = phi * x[t - 1] + rng.normal(0, sigma)
    return x


def test_adf_tells_a_random_walk_from_a_stationary_series() -> None:
    assert adf(ar1(1000, 0.5)).stationary()
    assert not adf(np.cumsum(np.random.default_rng(1).normal(size=1000))).stationary()


def test_halflife_of_ar1() -> None:
    # phi = 0.9 → lambda ≈ −0.1 → half-life ≈ ln2/0.1 = 6.9
    hl = halflife_ar1(ar1(5000, 0.9, seed=3))
    assert hl == pytest.approx(6.93, rel=0.15)
    assert math.isinf(halflife_ar1(np.arange(100.0)))


def test_engle_granger_recovers_the_hedge_ratio() -> None:
    rng = np.random.default_rng(5)
    x = 50 + np.cumsum(rng.normal(size=2000))
    y = 3 + 1.6 * x + ar1(2000, 0.7, seed=6)
    eg = engle_granger(y, x)
    assert eg.hedge_ratio == pytest.approx(1.6, abs=0.05)
    assert eg.cointegrated()


def test_johansen_finds_one_relation() -> None:
    rng = np.random.default_rng(8)
    x = 50 + np.cumsum(rng.normal(size=1500))
    y = 1.6 * x + ar1(1500, 0.6, seed=9)
    j = johansen(pd.DataFrame({"y": y, "x": x}))
    assert j.rank >= 1
    assert len(j.eigenvector) == 2


def test_kelly() -> None:
    assert kelly_leverage(0.001, 0.0001) == pytest.approx(10.0)
    assert half_kelly(0.001, 0.0001) == pytest.approx(5.0)
    assert half_kelly(0.001, 0.0001, cap=0.9) == 0.9
    assert half_kelly(-0.001, 0.0001) == 0.0
    with pytest.raises(ValueError):
        kelly_leverage(0.1, 0)
