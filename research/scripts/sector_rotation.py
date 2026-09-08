"""Sector rotation: dual momentum over the S&P sector ETFs, with GLD as the haven.

    uv run python scripts/sector_rotation.py                 # fetch from Yahoo
    uv run python scripts/sector_rotation.py --csv-dir DIR   # Kaggle-format CSVs, no network
    uv run python scripts/sector_rotation.py --universe XLB,XLC,XLE,XLF,XLI,XLK,XLP,XLRE,XLU,XLV,XLY,GLD

The same ``dual_momentum`` strategy the runtime already has, on a materially
different universe: every ``rebalance_days`` bars the sectors are ranked by
trailing return and the top ``top_k`` that beat the haven are held. A new
study, not a new strategy — the spec it writes names ``sector_rotation`` as
its ``research_study`` so re-validation runs this script and not
``dual_momentum.py``. The grid is six candidates; the test window is touched
once, under the next-open fill. The sweep, the out-of-sample numbers and
the cost stress go under ``--out-dir``.
"""

from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from tt.backtest.engine import Fill
from tt.studies import dual_momentum

SETUP = dual_momentum.Setup(
    base="sector_rotation",
    script="sector_rotation.py",
    # The nine Select Sector SPDRs listed in 1998. XLRE (2015) and XLC (2018)
    # exist but would cut the aligned history to their listing date; add them
    # with --universe once the training window can afford it.
    default_universe=["XLB", "XLE", "XLF", "XLI", "XLK", "XLP", "XLU", "XLV", "XLY", "GLD"],
    start=dt.date(2005, 1, 1),
    fill=Fill.NEXT_OPEN,
    # Six to twelve months of momentum, two or three sectors, monthly turns.
    lookbacks=[126, 189, 252],
    top_ks=[2, 3],
    rebalances=[21],
)

if __name__ == "__main__":
    raise SystemExit(dual_momentum.run(__doc__ or "", SETUP))
