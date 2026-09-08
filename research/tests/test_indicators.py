"""Hand-computed cases for docs/CONTRACTS.md "Indicators"."""

import numpy as np
import pytest

from tt import indicators as ind


def nan_prefix(a: np.ndarray, k: int) -> bool:
    return bool(np.isnan(a[:k]).all()) and not np.isnan(a[k:]).any()


def test_rolling_mean_sd_zscore() -> None:
    x = np.array([1.0, 2, 4, 7, 7])
    assert nan_prefix(ind.rolling_mean(x, 3), 2)
    assert ind.rolling_mean(x, 3)[2:].tolist() == pytest.approx([7 / 3, 13 / 3, 6])
    assert ind.rolling_sample_sd(x, 2)[1:].tolist() == pytest.approx([np.sqrt(0.5), np.sqrt(2), np.sqrt(4.5), 0.0])
    z = ind.rolling_zscore(x, 2)
    assert np.isnan(z[0]) and np.isnan(z[4])  # short window; sd of (7, 7) is 0
    assert z[1] == pytest.approx((2 - 1.5) / np.sqrt(0.5))
    with pytest.raises(ValueError):
        ind.rolling_mean(x, 1)


def test_rolling_return() -> None:
    p = np.array([100.0, 110, 99, 108.9])
    r = ind.rolling_return(p, 2)
    assert nan_prefix(r, 2) and r[2:].tolist() == pytest.approx([-0.01, -0.01])
    assert ind.rolling_return(p, 1)[1:].tolist() == pytest.approx([0.1, -0.1, 0.1])
    assert np.isnan(ind.rolling_return(p, 4)).all()
    with pytest.raises(ValueError):
        ind.rolling_return(p, 0)


def test_sma_is_exact_on_constant_prices() -> None:
    s = ind.sma(np.array([10.0, 10, 10, 10]), 3)
    assert nan_prefix(s, 2) and s[2] == 10.0 and s[3] == 10.0
    assert ind.sma(np.array([1.0, 2, 3, 4]), 2)[1:].tolist() == [1.5, 2.5, 3.5]
    assert np.isnan(ind.sma(np.array([1.0]), 2)).all()


def test_donchian_uses_prior_bars_only() -> None:
    h = np.array([5.0, 7, 6, 9, 4])
    up = ind.donchian_upper(h, 2)
    assert nan_prefix(up, 2) and up[2:].tolist() == [7, 7, 9]
    lo = ind.donchian_lower(np.array([5.0, 3, 6, 2, 4]), 3)
    assert nan_prefix(lo, 3) and lo[3:].tolist() == [3, 2]
    # A bar's own extreme is never in its channel: the 9 at t=3 shows up at t=4.
    assert up[3] == 7 and up[4] == 9


def test_realised_vol_matches_numpy() -> None:
    p = np.array([100.0, 110, 99, 108.9, 108.9, 120])
    v = ind.realised_vol(p, 2)
    r = p[1:] / p[:-1] - 1
    assert nan_prefix(v, 2)
    assert v[2] == pytest.approx(np.std(r[0:2], ddof=1))
    assert v[5] == pytest.approx(np.std(r[3:5], ddof=1))
    # A flat stretch has a zero sd, which is defined.
    assert ind.realised_vol(np.array([1.0, 1, 1, 1]), 2)[2:].tolist() == [0.0, 0.0]
    assert np.isnan(ind.realised_vol(np.array([1.0]), 2)).all()


def test_true_range_and_atr() -> None:
    h = np.array([10.0, 12, 11, 15])
    lo = np.array([9.0, 10, 9, 12])
    c = np.array([9.5, 11, 10, 14])
    tr = ind.true_range(h, lo, c)
    assert np.isnan(tr[0])
    # t=1: max(2, |12-9.5|, |10-9.5|) = 2.5; t=2: max(2, 0, 2) = 2; t=3: max(3, 5, 2) = 5
    assert tr[1:].tolist() == [2.5, 2.0, 5.0]
    a = ind.atr(h, lo, c, 2)
    assert np.isnan(a[:2]).all() and a[2] == 2.25 and a[3] == (2.25 + 5) / 2


def test_wilder_rsi_by_hand_and_edge_cases() -> None:
    px = np.array([10.0, 11, 10, 12, 12, 9])
    out = ind.wilder_rsi(px, 2)
    assert nan_prefix(out, 2)
    assert out[2] == 50.0                                   # +1, -1
    assert out[3] == pytest.approx(100 - 100 / (1 + 1.25 / 0.25))
    assert out[4] == pytest.approx(100 - 100 / (1 + 0.625 / 0.125))
    assert out[5] == pytest.approx(100 - 100 / (1 + 0.3125 / 1.5625))
    assert ind.wilder_rsi(np.array([5.0, 6, 7, 8]), 2)[2:].tolist() == [100.0, 100.0]
    assert ind.wilder_rsi(np.array([5.0, 5, 5, 5]), 2)[2:].tolist() == [50.0, 50.0]
    assert ind.wilder_rsi(np.array([5.0, 4, 3]), 2)[2] == 0.0
    assert np.isnan(ind.wilder_rsi(np.array([5.0, 6]), 2)).all()
