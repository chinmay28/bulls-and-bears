"""``sma_trend``, exactly as docs/CONTRACTS.md defines it.

Faber's timing rule (*A Quantitative Approach to Tactical Asset Allocation*,
2007), one sleeve per risk asset: hold the asset while its price is above
its own trailing moving average, and the haven — the universe's last symbol —
otherwise. An optional band around the average keeps a price that hugs it
from flipping the sleeve every day.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.strategies.ratio import align, split, weights_frame

NAME = "sma_trend"

KNOWN = {"lookback", "band"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    lookback: int
    band: float

    @classmethod
    def from_dict(cls, d: dict[str, float]) -> Params:
        """Build from a spec's ``params`` mapping, refusing unknown keys."""
        unknown = set(d) - KNOWN
        if unknown:
            raise ValueError(f"unknown params {sorted(unknown)}")
        missing = KNOWN - set(d)
        if missing:
            raise ValueError(f"missing params {sorted(missing)}")
        p = cls(lookback=int(d["lookback"]), band=float(d["band"]))
        if p.lookback < 2 or p.lookback != d["lookback"]:
            raise ValueError("lookback must be a whole number >= 2")
        if not 0 <= p.band < 1:
            raise ValueError("need 0 <= band < 1")
        return p


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the per-sleeve state machines over the aligned bars.

    Returns one row per aligned date with ``sma_<symbol>`` (NaN while the
    window is short), ``s_<symbol>`` (1 in the risk asset, 0 in the haven)
    for each risk asset, and ``w_<symbol>`` for every symbol.
    """
    risk, haven = split(universe)
    df = align(bars, universe)
    n = len(df)
    k = len(risk)
    out = pd.DataFrame({"date": df["date"]})
    in_risk = np.zeros(n, dtype=int)
    for s in risk:
        px = df[s].to_numpy(dtype=float)
        sma = moving_average(px, params.lookback)
        st_col = np.zeros(n, dtype=int)
        st = 0
        for t in range(n):
            st = step(st, px[t], sma[t], not np.isnan(sma[t]), params)
            st_col[t] = st
        out[f"sma_{s}"] = sma
        out[f"s_{s}"] = st_col
        out[f"w_{s}"] = np.where(st_col == 1, gross_leverage / k, 0.0)
        in_risk += st_col
    flat = (k - in_risk).astype(float)
    out[f"w_{haven}"] = gross_leverage * flat / k
    return out


def moving_average(px: np.ndarray, lookback: int) -> np.ndarray:
    """The trailing mean over ``lookback`` bars, NaN while the window is short.

    Computed from a running sum, ``(C_t - C_{t-L}) / L`` with ``C`` the
    in-order cumulative sum, because that is the one form both languages
    evaluate in exactly the same order: the Go side agrees bit for bit, so a
    price sitting exactly on its average reads the same on both sides.
    """
    n = len(px)
    out = np.full(n, np.nan)
    if n < lookback:
        return out
    cs = np.cumsum(px)
    out[lookback - 1] = cs[lookback - 1] / lookback
    out[lookback:] = (cs[lookback:] - cs[:-lookback]) / lookback
    return out


def step(s: int, price: float, sma: float, defined: bool, p: Params) -> int:
    """One bar of a sleeve: strictly above the band enters, strictly below it exits.

    A price exactly on the average, or inside the band, leaves the sleeve
    where it is, so even a zero band cannot flip it every bar.
    """
    if not defined:
        return 0
    if s == 1:
        if price < sma * (1 - p.band):
            return 0
        return 1
    if price > sma * (1 + p.band):
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


__all__ = ["NAME", "Params", "align", "moving_average", "replay", "signals", "split", "step",
           "switches", "weights_frame"]
