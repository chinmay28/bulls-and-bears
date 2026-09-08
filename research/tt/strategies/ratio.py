"""``ratio_reversion``, exactly as docs/CONTRACTS.md defines it.

Each risk asset in the universe is compared with the last symbol, the haven
(GLD in the first spec), through the z-score of its log price ratio against a
trailing window. A sleeve of equal size is held in the risk asset while the
ratio is stretched below its mean and has not yet reverted, and in the haven
otherwise. The book is always fully invested and never short.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

NAME = "ratio_reversion"

KNOWN = {"lookback", "entry_z", "exit_z", "max_hold_days"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    lookback: int
    entry_z: float
    exit_z: float
    max_hold_days: int

    @classmethod
    def from_dict(cls, d: dict[str, float]) -> Params:
        """Build from a spec's ``params`` mapping, refusing unknown keys."""
        unknown = set(d) - KNOWN
        if unknown:
            raise ValueError(f"unknown params {sorted(unknown)}")
        missing = KNOWN - set(d)
        if missing:
            raise ValueError(f"missing params {sorted(missing)}")
        p = cls(
            lookback=int(d["lookback"]),
            entry_z=float(d["entry_z"]),
            exit_z=float(d["exit_z"]),
            max_hold_days=int(d["max_hold_days"]),
        )
        if p.lookback < 2 or p.lookback != d["lookback"]:
            raise ValueError("lookback must be a whole number >= 2")
        if not p.entry_z > 0:
            raise ValueError("entry_z must be > 0")
        if not p.exit_z > -p.entry_z:
            raise ValueError("need exit_z > -entry_z")
        if p.max_hold_days < 1 or p.max_hold_days != d["max_hold_days"]:
            raise ValueError("max_hold_days must be a whole number >= 1")
        return p


def split(universe: list[str]) -> tuple[list[str], str]:
    """The risk assets and the haven: every symbol but the last, and the last."""
    if len(universe) < 2:
        raise ValueError("ratio_reversion needs at least one risk asset and the haven")
    if len(set(universe)) != len(universe):
        raise ValueError("universe repeats a symbol")
    return list(universe[:-1]), universe[-1]


def align(bars: dict[str, pd.DataFrame], universe: list[str]) -> pd.DataFrame:
    """Inner-join every symbol's adjclose on date: a ``date`` column, then one per symbol."""
    split(universe)
    out: pd.DataFrame | None = None
    for s in universe:
        f = bars[s][["date", "adjclose"]].rename(columns={"adjclose": s})
        out = f if out is None else out.merge(f, on="date", how="inner")
    assert out is not None
    return out.sort_values("date").reset_index(drop=True)


def zscores(aligned: pd.DataFrame, universe: list[str], lookback: int) -> pd.DataFrame:
    """The z-score of each risk asset's log ratio to the haven; NaN where undefined."""
    risk, haven = split(universe)
    ln_h = np.log(aligned[haven].to_numpy(dtype=float))
    out = pd.DataFrame({"date": aligned["date"]})
    for s in risk:
        x = pd.Series(np.log(aligned[s].to_numpy(dtype=float)) - ln_h)
        mean = x.rolling(lookback).mean()
        sd = x.rolling(lookback).std(ddof=1)  # sample sd, like Go
        z = (x - mean) / sd
        z[~(sd > 0)] = np.nan
        out[s] = z.to_numpy()
    return out


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the per-sleeve state machines over the aligned bars.

    Returns one row per aligned date with ``z_<symbol>`` (NaN where
    undefined), ``s_<symbol>`` (1 in the risk asset, 0 in the haven) for each
    risk asset, and ``w_<symbol>`` for every symbol in the universe.
    """
    risk, haven = split(universe)
    df = align(bars, universe)
    z = zscores(df, universe, params.lookback)
    n = len(df)
    k = len(risk)
    out = pd.DataFrame({"date": df["date"]})
    states: dict[str, np.ndarray] = {}
    for s in risk:
        zs = z[s].to_numpy()
        st_col = np.zeros(n, dtype=int)
        st, held = 0, 0
        for t in range(n):
            st, held = step(st, held, zs[t], not np.isnan(zs[t]), params)
            st_col[t] = st
        states[s] = st_col
        out[f"z_{s}"] = zs
        out[f"s_{s}"] = st_col
    in_risk = np.zeros(n, dtype=int)
    for s in risk:
        w = np.where(states[s] == 1, gross_leverage / k, 0.0)
        out[f"w_{s}"] = w
        in_risk += states[s]
    flat = (k - in_risk).astype(float)
    out[f"w_{haven}"] = gross_leverage * flat / k
    return out


def step(s: int, held: int, z: float, defined: bool, p: Params) -> tuple[int, int]:
    """One bar of a sleeve's state machine: exits before entries, never both."""
    if not defined:
        return 0, 0
    if s == 1:
        if z >= p.exit_z or held >= p.max_hold_days:
            return 0, 0
        return 1, held + 1
    if z <= -p.entry_z:
        return 1, 0
    return 0, 0


def signals(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Target weights in long form: ``date, symbol, target_weight``, every date."""
    tr = replay(bars, universe, params, gross_leverage)
    parts = [
        pd.DataFrame({"date": tr["date"], "symbol": s, "target_weight": tr[f"w_{s}"]})
        for s in universe
    ]
    long = pd.concat(parts)
    return long.sort_values(["date", "symbol"]).reset_index(drop=True)


def weights_frame(replayed: pd.DataFrame, universe: list[str]) -> pd.DataFrame:
    """The backtest engine's view of a replay: one weight column per symbol."""
    return pd.DataFrame({s: replayed[f"w_{s}"] for s in universe})


def switches(replayed: pd.DataFrame, universe: list[str]) -> int:
    """How many times any sleeve changed side: the strategy's trade count."""
    risk, _ = split(universe)
    n = 0
    for s in risk:
        st = replayed[f"s_{s}"].to_numpy()
        n += int(np.count_nonzero(np.diff(st)))
    return n
