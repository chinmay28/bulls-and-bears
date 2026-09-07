import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiError, api } from '../api'
import { TabPage } from '../components/Layout'
import { StrategyBadge } from '../components/status'
import { Banner, Empty, Field, Loading, Meter, Sheet, useLoader } from '../components/ui'
import { num, parts } from '../lib/format'
import type { Strategy } from '../types'

/** The specs the runtime found, armed or refused. Specs are written by the
 *  research side and read here; the phone can see why one was refused, and
 *  can import one research already wrote, but cannot author one — the server
 *  holds an import to the same schema, the same out-of-sample floor and the
 *  same TTL a run holds a spec to, so nothing arrives here that a run would
 *  only refuse. */
export default function Strategies() {
  const { data, error, loading, offline, reload } = useLoader(() => api.strategies(), [], 10000)
  const [importing, setImporting] = useState(false)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}

      {data?.length === 0 && (
        <Empty
          message="No specs found. A spec is a YAML file in specs/ with its backtest window, out-of-sample Sharpe and a TTL; the runtime refuses anything missing those, and anything with a Sharpe under 1.0."
          action={
            <button className="primary" onClick={() => setImporting(true)}>
              Import a spec
            </button>
          }
        />
      )}

      {data?.map((s) => <StrategyCard key={s.name} s={s} />)}

      {data && data.length > 0 && (
        <button className="secondary block" style={{ marginTop: 12 }} onClick={() => setImporting(true)}>
          Import a spec
        </button>
      )}

      {importing && (
        <ImportSheet
          onClose={() => setImporting(false)}
          onDone={() => {
            setImporting(false)
            reload()
          }}
        />
      )}
    </TabPage>
  )
}

/** ImportSheet takes the YAML research wrote and hands it to the server,
 *  which decides. Everything it can say back — not a spec, Sharpe under the
 *  floor, expired, already installed — is shown verbatim: the server's
 *  reasons are better than any this screen could invent. */
function ImportSheet({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [yaml, setYaml] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)

  const submit = async (replace: boolean) => {
    setBusy(true)
    setError('')
    try {
      await api.importStrategy(yaml, replace)
      onDone()
    } catch (e) {
      const message = e instanceof ApiError ? e.message : String(e)
      setError(message)
      setConflict(e instanceof ApiError && e.status === 409)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      title="Import a spec"
      subtitle="Paste the YAML from specs/ that research wrote. It is checked against the same schema, Sharpe floor and TTL a run applies."
      onClose={onClose}
    >
      {error && (
        <div style={{ marginBottom: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      <Field label="Spec YAML" help="Nothing is written unless the runtime would actually run it.">
        <textarea
          value={yaml}
          onChange={(e) => {
            setYaml(e.target.value)
            setConflict(false)
          }}
          rows={12}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          className="mono"
          placeholder={'name: gld_gdx_pairs\nversion: 1\nstrategy: pairs_zscore\n…'}
        />
      </Field>
      <button className="primary block" disabled={busy || yaml.trim() === ''} onClick={() => submit(false)}>
        {busy ? 'Importing…' : 'Import'}
      </button>
      {conflict && (
        <button className="secondary block" style={{ marginTop: 8 }} disabled={busy} onClick={() => submit(true)}>
          Replace the installed spec
        </button>
      )}
    </Sheet>
  )
}

function StrategyCard({ s }: { s: Strategy }) {
  const p = s.provenance
  return (
    <Link className="card" to={`/strategies/${s.name}`}>
      <div className="row between">
        <div className="grow">
          <div className="title">{s.name}</div>
          <div className="sub">
            {s.status === 'armed'
              ? parts(s.strategy, s.universe?.join(' / '), p && `Sharpe ${num(p.oosSharpe, 2)}`)
              : parts(s.strategy ?? s.path, s.reason)}
          </div>
        </div>
        <StrategyBadge status={s.status} />
      </div>
      {p && (
        <div style={{ marginTop: 12 }}>
          <Meter
            label="Spec fresh for"
            value={(s.freshDays / p.ttlDays) * 100}
            display={s.freshDays > 0 ? `${Math.floor(s.freshDays)} of ${p.ttlDays} days` : 'expired'}
            tone="progress"
          />
        </div>
      )}
      {s.signal && (
        <div className="sub" style={{ marginTop: 10 }}>
          {parts(
            s.signal.zDefined ? `z ${num(s.signal.z, 2)}` : 'z undefined',
            s.signal.state === 0 ? 'flat' : s.signal.state > 0 ? 'long the spread' : 'short the spread',
            `bars through ${s.signal.lastBar}`,
            s.signal.stale && 'stale',
          )}
        </div>
      )}
    </Link>
  )
}
