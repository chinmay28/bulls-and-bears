"""``time_series_momentum``, exactly as docs/CONTRACTS.md defines it.

Time-series momentum (Moskowitz, Ooi and Pedersen, *Time Series Momentum*,
2012; Hurst, Ooi and Pedersen, *A Century of Evidence on Trend-Following
Investing*, 2017), long-only, one sleeve per risk asset: every
``rebalance_days`` bars each asset is held if its own trailing
``lookback``-bar return is positive and rests in the haven — the universe's
last symbol — otherwise. Unlike ``dual_momentum`` no sleeve looks at another
asset or at the haven's return; the threshold is zero and is not a parameter.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.indicators import rolling_return
from tt.strategies.ratio import align, split, weights_frame

NAME = "time_series_momentum"

KNOWN = {"lookback", "rebalance_days"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    lookback: int
    rebalance_days: int

    @classmethod
    def from_dict(cls, d: dict[str, float]) -> Params:
        """Build from a spec's ``params`` mapping, refusing unknown keys."""
        unknown = set(d) - KNOWN
        if unknown:
            raise ValueError(f"unknown params {sorted(unknown)}")
        missing = KNOWN - set(d)
        if missing:
            raise ValueError(f"missing params {sorted(missing)}")
        p = cls(lookback=int(d["lookback"]), rebalance_days=int(d["rebalance_days"]))
        if p.lookback < 1 or p.lookback != d["lookback"]:
            raise ValueError("lookback must be a whole number >= 1")
        if p.rebalance_days < 1 or p.rebalance_days != d["rebalance_days"]:
            raise ValueError("rebalance_days must be a whole number >= 1")
        return p


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the rebalance schedule over the aligned bars.

    Returns one row per aligned date with ``r_<symbol>`` (the trailing
    return, NaN while undefined) and ``s_<symbol>`` (1 held, 0 in the haven)
    for each risk asset, ``rebalance`` (1 on a rebalance bar), and
    ``w_<symbol>`` for every symbol.
    """
    risk, haven = split(universe)
    df = align(bars, universe)
    n = len(df)
    k = len(risk)
    lb, rb = params.lookback, params.rebalance_days
    out = pd.DataFrame({"date": df["date"]})
    rebalance = np.array([1 if t >= lb and (t - lb) % rb == 0 else 0 for t in range(n)], dtype=int)
    out["rebalance"] = rebalance
    in_risk = np.zeros(n, dtype=int)
    for s in risk:
        r = rolling_return(df[s].to_numpy(dtype=float), lb)
        st_col = np.zeros(n, dtype=int)
        st = 0
        for t in range(n):
            if rebalance[t]:
                st = 1 if r[t] > 0 else 0
            st_col[t] = st
        out[f"r_{s}"] = r
        out[f"s_{s}"] = st_col
        out[f"w_{s}"] = np.where(st_col == 1, gross_leverage / k, 0.0)
        in_risk += st_col
    flat = (k - in_risk).astype(float)
    out[f"w_{haven}"] = gross_leverage * flat / k
    return out


def signals(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Target weights in long form: ``date, symbol, target_weight``, every date."""
    tr = replay(bars, universe, params, gross_leverage)
    parts = [
        pd.DataFrame({"date": tr["date"], "symbol": s, "target_weight": tr[f"w_{s}"]})
        for s in universe
    ]
    return pd.concat(parts).sort_values(["date", "symbol"]).reset_index(drop=True)


def switches(replayed: pd.DataFrame, universe: list[str]) -> int:
    """How many times any sleeve changed side."""
    risk, _ = split(universe)
    return sum(int(np.count_nonzero(np.diff(replayed[f"s_{s}"].to_numpy()))) for s in risk)


__all__ = ["NAME", "Params", "align", "replay", "signals", "split", "switches", "weights_frame"]
