"""``risk_parity_trend``, exactly as docs/CONTRACTS.md defines it.

Inverse-volatility weights behind a trend filter (Asness, Frazzini and
Pedersen, *Leverage Aversion and Risk Parity*, 2012, for the sizing; Faber,
2007, for the filter), long-only, one sleeve per risk asset: every
``rebalance_days`` bars the assets above their own ``trend_lookback``-bar
average pool their sleeves and split them in proportion to one over their
``vol_lookback``-bar realised volatility; every other sleeve rests in the
haven — the universe's last symbol. The first strategy here whose weights
are not on/off.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.indicators import realised_vol, sma
from tt.strategies.ratio import align, split, weights_frame

NAME = "risk_parity_trend"

KNOWN = {"trend_lookback", "vol_lookback", "rebalance_days"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    trend_lookback: int
    vol_lookback: int
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
        p = cls(trend_lookback=int(d["trend_lookback"]), vol_lookback=int(d["vol_lookback"]),
                rebalance_days=int(d["rebalance_days"]))
        if p.trend_lookback < 2 or p.trend_lookback != d["trend_lookback"]:
            raise ValueError("trend_lookback must be a whole number >= 2")
        if p.vol_lookback < 2 or p.vol_lookback != d["vol_lookback"]:
            raise ValueError("vol_lookback must be a whole number >= 2")
        if p.rebalance_days < 1 or p.rebalance_days != d["rebalance_days"]:
            raise ValueError("rebalance_days must be a whole number >= 1")
        return p


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the rebalance schedule over the aligned bars.

    Returns one row per aligned date with ``sma_<symbol>``, ``vol_<symbol>``
    (NaN while undefined) and ``e_<symbol>`` (1 eligible at that bar) for
    each risk asset, ``rebalance`` (1 on a rebalance bar), and
    ``w_<symbol>`` for every symbol.
    """
    risk, haven = split(universe)
    df = align(bars, universe)
    n = len(df)
    k = len(risk)
    trend, vol, reb = params.trend_lookback, params.vol_lookback, params.rebalance_days
    warm = max(trend, vol)
    out = pd.DataFrame({"date": df["date"]})
    rebalance = np.array([1 if t >= warm and (t - warm) % reb == 0 else 0 for t in range(n)], dtype=int)
    out["rebalance"] = rebalance
    px = {s: df[s].to_numpy(dtype=float) for s in risk}
    smas = {s: sma(px[s], trend) for s in risk}
    vols = {s: realised_vol(px[s], vol) for s in risk}
    elig = {s: np.zeros(n, dtype=int) for s in risk}
    for s in risk:
        for t in range(n):
            a, v = smas[s][t], vols[s][t]
            if not np.isnan(a) and not np.isnan(v) and px[s][t] > a and v > 0:
                elig[s][t] = 1
    w = {s: np.zeros(n) for s in universe}
    cur = dict.fromkeys(universe, 0.0)
    cur[haven] = gross_leverage
    for t in range(n):
        if rebalance[t]:
            active = [s for s in risk if elig[s][t]]
            cur = weights_at(risk, haven, active, {s: float(vols[s][t]) for s in active}, k, gross_leverage)
        for s in universe:
            w[s][t] = cur[s]
    for s in risk:
        out[f"sma_{s}"] = smas[s]
        out[f"vol_{s}"] = vols[s]
        out[f"e_{s}"] = elig[s]
    for s in universe:
        out[f"w_{s}"] = w[s]
    return out


def weights_at(
    risk: list[str], haven: str, active: list[str], vol: dict[str, float], k: int, gross: float
) -> dict[str, float]:
    """The rebalance-bar weights, in the contract's order of operations."""
    n = len(active)
    raw = {s: 1.0 / vol[s] for s in active}
    sum_raw = 0.0
    for s in active:
        sum_raw += raw[s]
    active_budget = gross * n / k
    out = {}
    for s in risk:
        out[s] = active_budget * raw[s] / sum_raw if s in raw else 0.0
    out[haven] = gross * (k - n) / k
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
    """How many times any sleeve went from held to flat or back."""
    risk, _ = split(universe)
    return sum(int(np.count_nonzero(np.diff((replayed[f"w_{s}"].to_numpy() > 0).astype(int))))
               for s in risk)


__all__ = ["NAME", "Params", "align", "replay", "signals", "split", "switches", "weights_at",
           "weights_frame"]
