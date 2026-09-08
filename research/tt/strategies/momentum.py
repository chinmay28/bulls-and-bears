"""``dual_momentum``, exactly as docs/CONTRACTS.md defines it.

Antonacci's dual momentum over a small universe: every ``rebalance_days``
bars, rank the risk assets by their trailing ``lookback``-bar return, keep
the top ``top_k`` of those that beat the haven's own trailing return, and
put the rest of the book in the haven — the universe's last symbol. Between
rebalances the holdings stand.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.strategies.ratio import align, split, weights_frame

NAME = "dual_momentum"

KNOWN = {"lookback", "top_k", "rebalance_days"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    lookback: int
    top_k: int
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
        p = cls(lookback=int(d["lookback"]), top_k=int(d["top_k"]),
                rebalance_days=int(d["rebalance_days"]))
        if p.lookback < 1 or p.lookback != d["lookback"]:
            raise ValueError("lookback must be a whole number >= 1")
        if p.top_k < 1 or p.top_k != d["top_k"]:
            raise ValueError("top_k must be a whole number >= 1")
        if p.rebalance_days < 1 or p.rebalance_days != d["rebalance_days"]:
            raise ValueError("rebalance_days must be a whole number >= 1")
        return p


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the rebalance schedule over the aligned bars.

    Returns one row per aligned date with ``r_<symbol>`` (the trailing
    return, NaN while undefined) for every symbol, ``h_<symbol>`` (1 held,
    0 not) for each risk asset, and ``w_<symbol>`` for every symbol.
    """
    risk, haven = split(universe)
    if params.top_k > len(risk):
        raise ValueError(f"top_k {params.top_k} exceeds the {len(risk)} risk assets")
    df = align(bars, universe)
    n = len(df)
    lb = params.lookback
    px = {s: df[s].to_numpy(dtype=float) for s in universe}
    ret = {s: np.full(n, np.nan) for s in universe}
    for s in universe:
        p = px[s]
        if n > lb:
            ret[s][lb:] = p[lb:] / p[:-lb] - 1
    held = {s: np.zeros(n, dtype=int) for s in risk}
    chosen: list[str] = []
    for t in range(n):
        if t >= lb and (t - lb) % params.rebalance_days == 0:
            chosen = pick(risk, {s: float(ret[s][t]) for s in universe}, haven, params.top_k)
        for s in chosen:
            held[s][t] = 1
    out = pd.DataFrame({"date": df["date"]})
    for s in universe:
        out[f"r_{s}"] = ret[s]
    count = np.zeros(n, dtype=int)
    for s in risk:
        out[f"h_{s}"] = held[s]
        out[f"w_{s}"] = np.where(held[s] == 1, gross_leverage / params.top_k, 0.0)
        count += held[s]
    out[f"w_{haven}"] = gross_leverage * (params.top_k - count).astype(float) / params.top_k
    return out


def pick(risk: list[str], ret: dict[str, float], haven: str, top_k: int) -> list[str]:
    """The risk assets to hold: those beating the haven, best first, at most top_k.

    Ties keep universe order (the sort is stable), as the Go side's does.
    """
    ahead = [s for s in risk if ret[s] > ret[haven]]
    ahead.sort(key=lambda s: -ret[s])
    return ahead[:top_k]


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
    """How many times any holding changed: the strategy's trade count."""
    risk, _ = split(universe)
    return sum(int(np.count_nonzero(np.diff(replayed[f"h_{s}"].to_numpy()))) for s in risk)


__all__ = ["NAME", "Params", "align", "pick", "replay", "signals", "split", "switches",
           "weights_frame"]
