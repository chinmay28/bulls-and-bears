"""``donchian_breakout``, exactly as docs/CONTRACTS.md defines it.

The Turtle channel breakout (Faith, *Way of the Turtle*, 2007; Clenow,
*Following the Trend*, 2013), long-only, one sleeve per risk asset: hold
the asset once it closes above the highest high of the prior
``entry_lookback`` bars, and leave for the haven — the universe's last
symbol — once it closes below the lowest low of the prior ``exit_lookback``
bars. The channels read the adjusted high and low and never include the
bar being judged.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.data import adjust
from tt.indicators import donchian_lower, donchian_upper
from tt.strategies.ratio import align, split, weights_frame

NAME = "donchian_breakout"

KNOWN = {"entry_lookback", "exit_lookback"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    entry_lookback: int
    exit_lookback: int

    @classmethod
    def from_dict(cls, d: dict[str, float]) -> Params:
        """Build from a spec's ``params`` mapping, refusing unknown keys."""
        unknown = set(d) - KNOWN
        if unknown:
            raise ValueError(f"unknown params {sorted(unknown)}")
        missing = KNOWN - set(d)
        if missing:
            raise ValueError(f"missing params {sorted(missing)}")
        p = cls(entry_lookback=int(d["entry_lookback"]), exit_lookback=int(d["exit_lookback"]))
        if p.entry_lookback < 2 or p.entry_lookback != d["entry_lookback"]:
            raise ValueError("entry_lookback must be a whole number >= 2")
        if p.exit_lookback < 2 or p.exit_lookback != d["exit_lookback"]:
            raise ValueError("exit_lookback must be a whole number >= 2")
        if not p.exit_lookback < p.entry_lookback:
            raise ValueError("need exit_lookback < entry_lookback")
        return p


def align_ohlc(bars: dict[str, pd.DataFrame], universe: list[str]) -> pd.DataFrame:
    """Inner-join on date: ``date``, then ``<s>``, ``high_<s>``, ``low_<s>`` per symbol.

    ``<s>`` is adjclose; the highs and lows are adjusted (tt.data.adjust).
    """
    out = align(bars, universe)
    hi = adjust.aligned(bars, universe, "high")
    lo = adjust.aligned(bars, universe, "low")
    for s in universe:
        out = out.merge(hi[["date", s]].rename(columns={s: f"high_{s}"}), on="date", how="inner")
        out = out.merge(lo[["date", s]].rename(columns={s: f"low_{s}"}), on="date", how="inner")
    return out.sort_values("date").reset_index(drop=True)


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the per-sleeve state machines over the aligned bars.

    Returns one row per aligned date with ``upper_<symbol>`` and
    ``lower_<symbol>`` (NaN while undefined), ``s_<symbol>`` (1 in the risk
    asset, 0 in the haven) for each risk asset, and ``w_<symbol>`` for every
    symbol.
    """
    risk, haven = split(universe)
    df = align_ohlc(bars, universe)
    n = len(df)
    k = len(risk)
    out = pd.DataFrame({"date": df["date"]})
    in_risk = np.zeros(n, dtype=int)
    for s in risk:
        close = df[s].to_numpy(dtype=float)
        upper = donchian_upper(df[f"high_{s}"].to_numpy(dtype=float), params.entry_lookback)
        lower = donchian_lower(df[f"low_{s}"].to_numpy(dtype=float), params.exit_lookback)
        st_col = np.zeros(n, dtype=int)
        st = 0
        for t in range(n):
            st = step(st, close[t], upper[t], lower[t])
            st_col[t] = st
        out[f"upper_{s}"] = upper
        out[f"lower_{s}"] = lower
        out[f"s_{s}"] = st_col
        out[f"w_{s}"] = np.where(st_col == 1, gross_leverage / k, 0.0)
        in_risk += st_col
    flat = (k - in_risk).astype(float)
    out[f"w_{haven}"] = gross_leverage * flat / k
    return out


def step(s: int, close: float, upper: float, lower: float) -> int:
    """One bar of a sleeve: strictly above the entry channel enters, strictly below the exit channel exits.

    An undefined entry channel forces the sleeve flat. A close exactly on a
    channel leaves the sleeve where it is.
    """
    if np.isnan(upper):
        return 0
    if s == 1:
        if close < lower:
            return 0
        return 1
    if close > upper:
        return 1
    return 0


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


__all__ = ["NAME", "Params", "align", "align_ohlc", "replay", "signals", "split", "step",
           "switches", "weights_frame"]
