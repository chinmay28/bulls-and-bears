#!/usr/bin/env python3
"""How often a hold between two times of day paid, for each symbol.

    uv run python scripts/intraday_window.py --universe VTI,QQQ --window 10:00-15:00

10:00 to 15:00 in New York is 7am to noon on the Pacific coast, all year: the
times are read on the exchange's clock, so a daylight-saving change moves
both together. Yahoo serves only 60 days of 30-minute bars and 730 of hourly
ones, so the exact window is a small sample and the hourly 10:30-15:30 one is
the long view; the printed standard error is the number to read first.

Nothing here is promoted: intraday bars are unadjusted, this writes no spec
and no golden, and a 5-hour hold is a question, not a strategy.
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys

from tt.data import intraday
from tt.stats import session

# A market order on a penny-wide ETF pays about half the spread on each side.
DEFAULT_COST_BPS = 2.0


def window(text: str) -> tuple[dt.time, dt.time]:
    lo, _, hi = text.partition("-")
    if not hi:
        raise argparse.ArgumentTypeError(f"a window looks like 10:00-15:00, not {text!r}")
    try:
        return dt.time.fromisoformat(lo.strip()), dt.time.fromisoformat(hi.strip())
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"{text!r}: {e}") from e


def symbols(text: str) -> list[str]:
    out = [s.strip().upper() for s in text.replace(" ", ",").split(",") if s.strip()]
    if not out:
        raise argparse.ArgumentTypeError("name at least one symbol")
    if len(set(out)) != len(out):
        raise argparse.ArgumentTypeError("a universe cannot repeat a symbol")
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--universe", type=symbols, default=["VTI", "QQQ"])
    ap.add_argument("--window", type=window, action="append", metavar="HH:MM-HH:MM",
                    help="repeatable; default 10:00-15:00")
    ap.add_argument("--interval", default="30m", choices=sorted(intraday.MAX_LOOKBACK_DAYS))
    ap.add_argument("--days", type=int, default=60, help="calendar days back (default %(default)s)")
    ap.add_argument("--cost-bps", type=float, default=DEFAULT_COST_BPS,
                    help="round-trip cost in basis points (default %(default)s)")
    args = ap.parse_args()
    windows = args.window or [(dt.time(10, 0), dt.time(15, 0))]

    try:
        intraday.check_period(args.interval, args.days)
        for entry, exit_at in windows:
            intraday.check_grid(args.interval, entry)
            intraday.check_grid(args.interval, exit_at)
        bars = intraday.fetch(args.universe, interval=args.interval, days=args.days)
    except (ValueError, RuntimeError) as e:
        print(f"REFUSED: {e}", file=sys.stderr)
        return 2

    print(f"{args.interval} bars from Yahoo, {args.days} calendar days back, "
          f"{intraday.EXCHANGE_TZ} clock; cost {args.cost_bps:g} bp round trip")
    for symbol in args.universe:
        for entry, exit_at in windows:
            s = session.sessions(bars[symbol], entry, exit_at)
            label = f"{symbol} {entry:%H:%M}-{exit_at:%H:%M}"
            if s.frame.empty:
                print(f"{label:<22} no session had a bar at both times", file=sys.stderr)
                continue
            print(session.summarize(s.returns).line(f"{label} gross"))
            print(session.summarize(session.net(s.returns, args.cost_bps)).line(f"{label} net"))
            if s.dropped:
                first, last = s.frame["date"].iloc[0], s.frame["date"].iloc[-1]
                print(f"{'':<22} {first}..{last}, {len(s.dropped)} sessions dropped "
                      f"(early closes and short days)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
