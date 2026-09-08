"""Antonacci's dual momentum over a small ETF universe, with GLD as the haven.

    uv run python scripts/dual_momentum.py                 # fetch from Yahoo
    uv run python scripts/dual_momentum.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/dual_momentum.py --universe SPY,EFA,AGG,BIL

Every ``rebalance_days`` bars the risk assets are ranked by trailing return;
the top ``top_k`` that beat the haven's own trailing return are held, the rest
of the book sits in the haven (docs/CONTRACTS.md, ``dual_momentum``). The
lookback, the number held and the rebalance interval are chosen on the
training window from a fixed grid; the test window is touched once. The spec
is promoted only when the out-of-sample Sharpe clears 1.0 on Yahoo data. The
sweep, the out-of-sample numbers and the cost stress go under ``--out-dir``.
The body is ``tt.studies.dual_momentum``, which ``sector_rotation.py`` shares.
"""

from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt.backtest.engine import Fill
from tt.studies import dual_momentum

SETUP = dual_momentum.Setup(
    base="dual_momentum",
    script="dual_momentum.py",
    default_universe=["SPY", "QQQ", "VTI", "XLK", "GLD"],
    start=dt.date(2005, 1, 1),
    # Researched with the same-close loop (docs/CONTRACTS.md, Execution); a
    # re-run under next_open is a separate change with its own goldens.
    fill=Fill.SAME_CLOSE,
    # Three to twelve months of momentum, one or two holdings, weekly or monthly turns.
    lookbacks=[63, 126, 189, 252],
    top_ks=[1, 2],
    rebalances=[5, 21],
)

if __name__ == "__main__":
    raise SystemExit(dual_momentum.run(__doc__ or "", SETUP))
