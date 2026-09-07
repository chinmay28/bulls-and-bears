"""Writing what research found: a spec the runtime will accept, and goldens.

A spec is only ever written by this module, validated against
``specs/strategy.schema.json`` on the way out, so the runtime never sees one
that is malformed — only ones it may still refuse on policy.
"""

from __future__ import annotations

import datetime as dt
import json
import subprocess
from pathlib import Path
from typing import Any

import jsonschema
import pandas as pd
import yaml

from tt.data import bars as barsio

REPO_ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = REPO_ROOT / "specs" / "strategy.schema.json"


def git_sha(cwd: Path = REPO_ROOT) -> str:
    """Short SHA of the research that produced a spec, or ``0000000`` outside git."""
    try:
        out = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"], cwd=cwd, capture_output=True, text=True,
            check=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        return "0000000"
    return out.stdout.strip()


def build_spec(
    *,
    name: str,
    strategy: str,
    universe: list[str],
    params: dict[str, float],
    gross_leverage: float,
    max_notional_per_leg_usd: float,
    train_window: tuple[dt.date, dt.date],
    test_window: tuple[dt.date, dt.date],
    oos_sharpe: float,
    oos_max_drawdown: float,
    commission_usd: float,
    slippage_bps: float,
    ttl_days: int = 90,
    generated_at: dt.datetime | None = None,
    research_git_sha: str | None = None,
    version: int = 1,
) -> dict[str, Any]:
    """Assemble a spec mapping in the schema's shape."""
    now = generated_at or dt.datetime.now(dt.UTC)
    return {
        "name": name,
        "version": version,
        "strategy": strategy,
        "universe": list(universe),
        "params": {k: float(v) for k, v in params.items()},
        "sizing": {
            "gross_leverage": float(gross_leverage),
            "max_notional_per_leg_usd": float(max_notional_per_leg_usd),
        },
        "provenance": {
            "train_window": {"from": train_window[0].isoformat(), "to": train_window[1].isoformat()},
            "test_window": {"from": test_window[0].isoformat(), "to": test_window[1].isoformat()},
            "oos_sharpe": float(oos_sharpe),
            "oos_max_drawdown": float(oos_max_drawdown),
            "cost_model": {"commission_usd": float(commission_usd), "slippage_bps": float(slippage_bps)},
            "research_git_sha": research_git_sha or git_sha(),
            "generated_at": now.astimezone(dt.UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "ttl_days": int(ttl_days),
        },
    }


def validate(spec: dict[str, Any], schema_path: Path = SCHEMA_PATH) -> None:
    """Raise ``jsonschema.ValidationError`` if the spec does not fit the schema."""
    schema = json.loads(schema_path.read_text())
    jsonschema.validate(spec, schema, format_checker=jsonschema.Draft202012Validator.FORMAT_CHECKER)


def write_spec(spec: dict[str, Any], path: Path, schema_path: Path = SCHEMA_PATH) -> None:
    """Validate and write a spec as YAML."""
    validate(spec, schema_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(yaml.safe_dump(spec, sort_keys=False))


def write_golden(
    directory: Path,
    *,
    bars: dict[str, pd.DataFrame],
    spec: dict[str, Any],
    signals: pd.DataFrame,
    equity: pd.DataFrame,
    metrics: dict[str, float | int],
    note: str | None = None,
) -> list[Path]:
    """Write the parity fixtures for one strategy; returns the files written."""
    directory.mkdir(parents=True, exist_ok=True)
    written: list[Path] = []
    for symbol, df in bars.items():
        p = directory / f"bars_{symbol}.parquet"
        barsio.write_bars(df, p)
        written.append(p)
    p = directory / "expected_signals.csv"
    signals.to_csv(p, index=False, float_format="%.17g")
    written.append(p)
    p = directory / "equity_curve.csv"
    equity.to_csv(p, index=False, float_format="%.17g")
    written.append(p)
    p = directory / "metrics.json"
    p.write_text(json.dumps(metrics, indent=2) + "\n")
    written.append(p)
    p = directory / "spec.yaml"
    p.write_text(yaml.safe_dump(spec, sort_keys=False))
    written.append(p)
    if note:
        p = directory / "README.md"
        p.write_text(note)
        written.append(p)
    return written
