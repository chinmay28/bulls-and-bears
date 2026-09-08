"""``rsi2_reversion``, exactly as docs/CONTRACTS.md defines it.

Connors and Alvarez's two-period RSI pullback (*Short Term Trading
Strategies That Work*, 2009; *High Probability ETF Trading*, 2009),
long-only, one sleeve per risk asset: while the asset closes above its long
moving average, buy it when Wilder's two-bar RSI says it is oversold, and
sell it back for the haven — the universe's last symbol — once the RSI has
recovered, the time stop is reached, or the close falls below the average.
The RSI period is two and is not a parameter.

The Go runtime implements the same text; ``golden/<name>/expected_signals.csv``
holds the two to 1e-9. Change this and the contract and the goldens together.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

from tt.indicators import sma, wilder_rsi
from tt.strategies.ratio import align, split, weights_frame

NAME = "rsi2_reversion"
RSI_PERIOD = 2

KNOWN = {"trend_lookback", "rsi_entry", "rsi_exit", "max_hold_days"}


@dataclass(frozen=True)
class Params:
    """The spec's params block, typed."""

    trend_lookback: int
    rsi_entry: float
    rsi_exit: float
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
        p = cls(trend_lookback=int(d["trend_lookback"]), rsi_entry=float(d["rsi_entry"]),
                rsi_exit=float(d["rsi_exit"]), max_hold_days=int(d["max_hold_days"]))
        if p.trend_lookback < 2 or p.trend_lookback != d["trend_lookback"]:
            raise ValueError("trend_lookback must be a whole number >= 2")
        if not 0 <= p.rsi_entry < p.rsi_exit <= 100:
            raise ValueError("need 0 <= rsi_entry < rsi_exit <= 100")
        if p.max_hold_days < 1 or p.max_hold_days != d["max_hold_days"]:
            raise ValueError("max_hold_days must be a whole number >= 1")
        return p


def replay(
    bars: dict[str, pd.DataFrame], universe: list[str], params: Params, gross_leverage: float
) -> pd.DataFrame:
    """Replay the per-sleeve state machines over the aligned bars.

    Returns one row per aligned date with ``rsi_<symbol>`` and
    ``sma_<symbol>`` (NaN while undefined), ``s_<symbol>`` (1 in the risk
    asset, 0 in the haven) for each risk asset, and ``w_<symbol>`` for every
    symbol.
    """
    risk, haven = split(universe)
    df = align(bars, universe)
    n = len(df)
    k = len(risk)
    out = pd.DataFrame({"date": df["date"]})
    in_risk = np.zeros(n, dtype=int)
    for s in risk:
        px = df[s].to_numpy(dtype=float)
        rsi = wilder_rsi(px, RSI_PERIOD)
        avg = sma(px, params.trend_lookback)
        st_col = np.zeros(n, dtype=int)
        st, held = 0, 0
        for t in range(n):
            st, held = step(st, held, px[t], rsi[t], avg[t], params)
            st_col[t] = st
        out[f"rsi_{s}"] = rsi
        out[f"sma_{s}"] = avg
        out[f"s_{s}"] = st_col
        out[f"w_{s}"] = np.where(st_col == 1, gross_leverage / k, 0.0)
        in_risk += st_col
    flat = (k - in_risk).astype(float)
    out[f"w_{haven}"] = gross_leverage * flat / k
    return out


def step(s: int, held: int, price: float, rsi: float, avg: float, p: Params) -> tuple[int, int]:
    """One bar of a sleeve's state machine: exits before entries, never both.

    In the asset: leave once the RSI is strictly above ``rsi_exit``, the
    time stop is hit, or the close is strictly below the average. In the
    haven: enter when the close is strictly above the average and the RSI
    strictly below ``rsi_entry``. Undefined inputs force the haven.
    """
    if np.isnan(rsi) or np.isnan(avg):
        return 0, 0
    if s == 1:
        if rsi > p.rsi_exit or held >= p.max_hold_days or price < avg:
            return 0, 0
        return 1, held + 1
    if price > avg and rsi < p.rsi_entry:
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
    return pd.concat(parts).sort_values(["date", "symbol"]).reset_index(drop=True)


def switches(replayed: pd.DataFrame, universe: list[str]) -> int:
    """How many times any sleeve changed side."""
    risk, _ = split(universe)
    return sum(int(np.count_nonzero(np.diff(replayed[f"s_{s}"].to_numpy()))) for s in risk)


def holding_periods(replayed: pd.DataFrame, universe: list[str]) -> list[int]:
    """The length in bars of every completed or open holding, across sleeves."""
    risk, _ = split(universe)
    out: list[int] = []
    for s in risk:
        st = replayed[f"s_{s}"].to_numpy()
        run = 0
        for v in st:
            if v == 1:
                run += 1
            elif run:
                out.append(run)
                run = 0
        if run:
            out.append(run)
    return out


__all__ = ["NAME", "RSI_PERIOD", "Params", "align", "holding_periods", "replay", "signals", "split",
           "step", "switches", "weights_frame"]
