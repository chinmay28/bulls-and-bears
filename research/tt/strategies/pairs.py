"""``pairs_zscore``, exactly as docs/CONTRACTS.md defines it.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

NAME = "pairs_zscore"


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    hedge_ratio: float
    lookback: int
    entry_z: float
    exit_z: float
    max_hold_days: int

    @classmethod
    def from_dict(cls, d: dict[str, float]) -> Params:
        """Build from a spec's ``params`` mapping, refusing unknown keys."""
        known = {"hedge_ratio", "lookback", "entry_z", "exit_z", "max_hold_days"}
        unknown = set(d) - known
        if unknown:
            raise ValueError(f"unknown params {sorted(unknown)}")
        p = cls(
            hedge_ratio=float(d["hedge_ratio"]),
            lookback=int(d["lookback"]),
            entry_z=float(d["entry_z"]),
            exit_z=float(d["exit_z"]),
            max_hold_days=int(d["max_hold_days"]),
        )
        if p.lookback < 2 or p.lookback != d["lookback"]:
            raise ValueError("lookback must be a whole number >= 2")
        if not 0 <= p.exit_z < p.entry_z:
            raise ValueError("need 0 <= exit_z < entry_z")
        if p.max_hold_days < 1 or p.max_hold_days != d["max_hold_days"]:
            raise ValueError("max_hold_days must be a whole number >= 1")
        return p


def align(bars: dict[str, pd.DataFrame], universe: list[str]) -> pd.DataFrame:
    """Inner-join the two symbols' adjclose on date: columns ``date, a, b``."""
    if len(universe) != 2:
        raise ValueError("pairs_zscore takes exactly two symbols")
    a, b = universe
    fa = bars[a][["date", "adjclose"]].rename(columns={"adjclose": "a"})
    fb = bars[b][["date", "adjclose"]].rename(columns={"adjclose": "b"})
    out = fa.merge(fb, on="date", how="inner").sort_values("date").reset_index(drop=True)
    return out


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the state machine over the aligned bars.

    Returns one row per aligned date with ``z`` (NaN where undefined),
    ``state`` and the two weights ``w_a``, ``w_b``.
    """
    df = align(bars, universe)
    n = len(df)
    pa_ = df["a"].to_numpy(dtype=float)
    pb_ = df["b"].to_numpy(dtype=float)
    h = params.hedge_ratio
    spread = pa_ - h * pb_
    s = pd.Series(spread)
    mean = s.rolling(params.lookback).mean().to_numpy()
    sd = s.rolling(params.lookback).std(ddof=1).to_numpy()  # sample sd, like Go
    z = np.full(n, np.nan)
    ok = ~np.isnan(sd) & (sd != 0)
    z[ok] = (spread[ok] - mean[ok]) / sd[ok]

    state = np.zeros(n, dtype=int)
    w_a = np.zeros(n)
    w_b = np.zeros(n)
    st, held = 0, 0
    for t in range(n):
        st, held = _step(st, held, z[t], bool(ok[t]), params)
        state[t] = st
        if st != 0:
            n_a, n_b = pa_[t], h * pb_[t]
            gross = n_a + n_b
            w_a[t] = st * gross_leverage * n_a / gross
            w_b[t] = -st * gross_leverage * n_b / gross
    return pd.DataFrame({"date": df["date"], "z": z, "state": state, "w_a": w_a, "w_b": w_b})


def _step(s: int, held: int, z: float, defined: bool, p: Params) -> tuple[int, int]:
    """One bar of the state machine: exits before entries, never both."""
    if not defined:
        return 0, 0
    if s == 1:
        if z >= -p.exit_z or held >= p.max_hold_days:
            return 0, 0
        return 1, held + 1
    if s == -1:
        if z <= p.exit_z or held >= p.max_hold_days:
            return 0, 0
        return -1, held + 1
    if z <= -p.entry_z:
        return 1, 0
    if z >= p.entry_z:
        return -1, 0
    return 0, 0


def signals(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Target weights in long form: ``date, symbol, target_weight``, every date."""
    tr = replay(bars, universe, params, gross_leverage)
    a, b = universe
    long = pd.concat(
        [
            pd.DataFrame({"date": tr["date"], "symbol": a, "target_weight": tr["w_a"]}),
            pd.DataFrame({"date": tr["date"], "symbol": b, "target_weight": tr["w_b"]}),
        ]
    )
    return long.sort_values(["date", "symbol"]).reset_index(drop=True)
