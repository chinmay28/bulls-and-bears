import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiError, api } from '../api'
import { TabPage } from '../components/Layout'
import { Badge, Banner, Card, Field, Loading, SectionTitle, useLoader } from '../components/ui'
import { parts, time } from '../lib/format'
import type { JobStatus, ResearchInfo, ResearchJob, Schedule, Study } from '../types'

/** How often a running job's log is pulled. */
const POLL_MS = 1500

/** The studies under research/scripts, run on the machine from here. The
 *  server finds or downloads uv, builds the Python environment inside its
 *  data directory, and runs the script with its output pointed at the specs
 *  and bars the runtime reads — so a study that clears the Sharpe floor shows
 *  up on Strategies by itself, and one that does not says why in its log.
 *  Nothing here can arm a spec the script's own gate would refuse. */
export default function Research() {
  const { data, error, offline, reload } = useLoader(() => api.research(), [], 5000)
  const [selected, setSelected] = useState<string | null>(null)
  const [actionError, setActionError] = useState('')

  const busy = !!data?.env.busy
  const jobId = selected ?? data?.current?.id ?? null

  const start = async (fn: () => Promise<ResearchJob>) => {
    setActionError('')
    try {
      const job = await fn()
      setSelected(job.id)
      reload()
    } catch (e) {
      setActionError(e instanceof ApiError ? e.message : String(e))
    }
  }

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />

      {data && (
        <>
          <SectionTitle>Environment</SectionTitle>
          <EnvironmentCard
            info={data}
            busy={busy}
            error={actionError}
            onSetup={() => start(() => api.researchSetup())}
          />

          {data.schedule && (
            <>
              <SectionTitle>Re-validation</SectionTitle>
              <ScheduleCard schedule={data.schedule} busy={busy} onChanged={reload} onShow={setSelected} />
            </>
          )}

          <SectionTitle>Studies</SectionTitle>
          {data.studies.map((study) => (
            <StudyCard
              key={study.name}
              study={study}
              disabled={busy || !data.env.tree}
              onRun={(trainTo, universe) => start(() => api.researchRun(study.name, trainTo, universe))}
            />
          ))}

          {jobId && (
            <>
              <SectionTitle>Job</SectionTitle>
              <JobCard id={jobId} onChanged={reload} />
            </>
          )}

          {data.logs.length > 0 && (
            <>
              <SectionTitle>Earlier jobs</SectionTitle>
              <Card>
                {data.logs.map((id, i) => (
                  <div key={id}>
                    {i > 0 && <div className="list-divider inset" />}
                    <button
                      className="row between"
                      style={{ minHeight: 44, width: '100%', background: 'none', border: 0, padding: 0, color: 'inherit', textAlign: 'left' }}
                      onClick={() => setSelected(id)}
                    >
                      <div className="grow">
                        <div className="title mono">{id}</div>
                        <div className="sub">{describeLog(id, data)}</div>
                      </div>
                      {id === jobId && <Badge tone="accent">Shown</Badge>}
                    </button>
                  </div>
                ))}
              </Card>
            </>
          )}
        </>
      )}
    </TabPage>
  )
}

/** What is known about a log: this process's jobs carry their status, older
 *  ones only their time. */
function describeLog(id: string, info: ResearchInfo): string {
  const job = info.current?.id === id ? info.current : info.recent.find((j) => j.id === id)
  if (!job) return 'from an earlier server process'
  return parts(job.kind === 'setup' ? 'set up' : job.study, job.status, time(job.started))
}

function EnvironmentCard({
  info,
  busy,
  error,
  onSetup,
}: {
  info: ResearchInfo
  busy: boolean
  error: string
  onSetup: () => void
}) {
  const { env } = info
  return (
    <Card>
      <div className="kv">
        <span className="k">Research tree</span>
        <span className="v mono" style={{ color: env.tree ? undefined : 'var(--bad)' }}>
          {env.tree ? env.dir : `missing: ${env.dir}`}
        </span>
      </div>
      <div className="kv">
        <span className="k">uv</span>
        <span className="v mono">{env.uv || 'not installed · downloaded on first run'}</span>
      </div>
      <div className="kv">
        <span className="k">Python environment</span>
        <span className="v">{env.synced ? 'ready' : 'not built yet'}</span>
      </div>
      <p className="sub" style={{ margin: '8px 0 12px' }}>
        Setting up fetches uv if the machine has none and installs the research dependencies, yfinance among
        them, into the data directory. A study does this itself when it needs to; the button is for doing it
        ahead of time.
      </p>
      {error && (
        <div style={{ marginBottom: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      <button className="secondary block" disabled={busy || !env.tree} onClick={onSetup}>
        {busy ? 'A job is running…' : env.synced ? 'Rebuild the environment' : 'Set up the environment'}
      </button>
    </Card>
  )
}

/** ScheduleCard is the monthly re-validation: each installed spec re-run
 *  with its training cutoff held fixed, so its parameters cannot drift and
 *  its out-of-sample window only grows. A spec that promotes again is
 *  refreshed; one that does not is left to expire at its TTL. */
function ScheduleCard({
  schedule,
  busy,
  onChanged,
  onShow,
}: {
  schedule: Schedule
  busy: boolean
  onChanged: () => void
  onShow: (jobId: string) => void
}) {
  const [error, setError] = useState('')
  const act = async (fn: () => Promise<unknown>) => {
    setError('')
    try {
      await fn()
      onChanged()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    }
  }
  const latest = new Map<string, Schedule['history'][number]>()
  for (const rec of schedule.history) if (!latest.has(rec.name)) latest.set(rec.name, rec)
  const names = [...new Set([...Object.keys(schedule.lastRun), ...schedule.due])].sort()
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{schedule.enabled ? 'Monthly, outside market hours' : 'Off'}</div>
          <div className="sub">
            Every installed spec is re-run once a month with its training cutoff fixed. One that clears the floor
            again is refreshed and stays armed; one that does not is left to expire at its TTL.
          </div>
        </div>
        {schedule.running ? (
          <Badge tone="accent" dot pulse>
            Running
          </Badge>
        ) : (
          <Badge tone={schedule.enabled ? 'good' : 'neutral'} dot={schedule.enabled}>
            {schedule.enabled ? 'On' : 'Off'}
          </Badge>
        )}
      </div>
      {names.length > 0 && (
        <div style={{ marginTop: 10 }}>
          {names.map((name) => {
            const rec = latest.get(name)
            const last = schedule.lastRun[name]
            return (
              <div className="kv" key={name}>
                <span className="k">{name}</span>
                <span className="v" style={{ color: rec?.status === 'failed' ? 'var(--bad)' : undefined }}>
                  {rec ? (
                    <button
                      style={{ background: 'none', border: 0, padding: 0, color: 'inherit', font: 'inherit', textAlign: 'right' }}
                      onClick={() => rec.jobId && onShow(rec.jobId)}
                    >
                      {parts(
                        last && time(last),
                        rec.error ? 'failed' : rec.outcome ? (rec.outcome.promoted ? 'promoted' : 'not promoted') : rec.status,
                      )}
                    </button>
                  ) : (
                    'due'
                  )}
                </span>
              </div>
            )
          })}
        </div>
      )}
      {error && (
        <div style={{ marginTop: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      <div className="row" style={{ gap: 8, marginTop: 12 }}>
        <button className="secondary grow" onClick={() => act(() => api.setSchedule(!schedule.enabled))}>
          {schedule.enabled ? 'Turn off' : 'Turn on'}
        </button>
        <button className="secondary grow" disabled={busy || schedule.running} onClick={() => act(() => api.revalidateNow())}>
          Re-validate all now
        </button>
      </div>
    </Card>
  )
}

/** The latest training cutoff a study accepts: a year before today, so the
 *  window it is judged on holds about a year of bars. The server refuses
 *  anything later; the field just does not offer it. */
function latestTrainTo(): string {
  const d = new Date()
  d.setUTCFullYear(d.getUTCFullYear() - 1)
  return d.toISOString().slice(0, 10)
}

/** parseUniverse reads "SPY, QQQ GLD" as the symbols the server will check. */
export function parseUniverse(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim().toUpperCase())
    .filter((s) => s.length > 0)
}

function StudyCard({
  study,
  disabled,
  onRun,
}: {
  study: Study
  disabled: boolean
  onRun: (trainTo?: string, universe?: string[]) => void
}) {
  const [trainTo, setTrainTo] = useState(study.defaultTrainTo ?? '')
  const [universeText, setUniverseText] = useState(study.defaultUniverse.join(', '))
  const universe = parseUniverse(universeText)
  const custom = universe.join(',') !== study.defaultUniverse.join(',')
  const sizeOk = study.universeSize ? universe.length === study.universeSize : universe.length >= 2
  return (
    <Card>
      <div className="title">{study.title}</div>
      <div className="sub" style={{ margin: '4px 0 10px' }}>{study.description}</div>
      <div className="sub mono" style={{ marginBottom: 10 }}>{study.script}</div>
      <Field label="Symbols" help={study.universeHint + ' Another universe gets its own spec name.'}>
        <input
          type="text"
          value={universeText}
          onChange={(e) => setUniverseText(e.target.value)}
          spellCheck={false}
          autoCapitalize="characters"
          autoCorrect="off"
          className="mono"
        />
      </Field>
      {study.defaultTrainTo && (
        <Field
          label="Train through"
          help="The last day of the training window. Parameters are chosen on it alone; everything after, at least a year of it, is the one look out of sample."
        >
          <input type="date" value={trainTo} max={latestTrainTo()} onChange={(e) => setTrainTo(e.target.value)} />
        </Field>
      )}
      <button
        className="primary block"
        disabled={disabled || !sizeOk}
        onClick={() => onRun(study.defaultTrainTo ? trainTo || undefined : undefined, custom ? universe : undefined)}
      >
        Run study
      </button>
    </Card>
  )
}

/** JobCard follows one job: it pulls the log from where it left off while
 *  the job runs, and shows how it ended. */
function JobCard({ id, onChanged }: { id: string; onChanged: () => void }) {
  const [job, setJob] = useState<ResearchJob | null>(null)
  const [log, setLog] = useState('')
  const [error, setError] = useState('')
  const next = useRef(0)
  const pre = useRef<HTMLPreElement>(null)

  useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    next.current = 0
    setLog('')
    setJob(null)
    setError('')

    const pull = async () => {
      try {
        const got = await api.researchJob(id, next.current)
        if (cancelled) return
        // A log that shrank on the server (a restart, a truncation) starts over.
        if (got.next < next.current) setLog('')
        if (got.log) setLog((prev) => prev + got.log)
        next.current = got.next
        setJob(got.job)
        setError('')
        if (got.job.status === 'running') {
          timer = setTimeout(pull, POLL_MS)
        } else if (job?.status === 'running') {
          onChanged()
        }
      } catch (e) {
        if (cancelled) return
        setError(e instanceof ApiError ? e.message : String(e))
      }
    }
    pull()
    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])

  // Keep the newest lines in view while the job writes them.
  useEffect(() => {
    if (job?.status === 'running' && pre.current) pre.current.scrollTop = pre.current.scrollHeight
  }, [log, job?.status])

  const cancel = async () => {
    try {
      setJob(await api.researchCancel(id))
      onChanged()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e))
    }
  }

  return (
    <Card>
      {error && (
        <div style={{ marginBottom: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}
      {job && (
        <>
          <div className="row between">
            <div className="grow">
              <div className="title">{job.kind === 'setup' ? 'Set up the environment' : job.study || id}</div>
                      <div className="sub">
                {parts(
                  time(job.started),
                  job.options.universe?.join(' / '),
                  job.options.trainTo && `train through ${job.options.trainTo}`,
                )}
              </div>
            </div>
            <JobBadge status={job.status} />
          </div>

          {job.steps.length > 0 && (
            <div style={{ marginTop: 10 }}>
              {job.steps.map((s) => (
                <div className="kv" key={s.name}>
                  <span className="k">{s.name}</span>
                  <span className="v">{s.status}</span>
                </div>
              ))}
            </div>
          )}

          {job.outcome && (
            <div style={{ marginTop: 12 }}>
              {job.outcome.promoted ? (
                <Banner tone="good">
                  Promoted. The spec is installed and the runtime will arm it. <Link to="/strategies">See it on Strategies.</Link>
                </Banner>
              ) : (
                <Banner tone="warn">{job.outcome.line}. The runtime would refuse this spec, so it was not installed.</Banner>
              )}
            </div>
          )}
          {job.status === 'failed' && job.error && (
            <div style={{ marginTop: 12 }}>
              <Banner tone="bad">{job.error}</Banner>
            </div>
          )}
          {job.status === 'unknown' && (
            <p className="sub" style={{ margin: '10px 0 0' }}>
              This job ran under an earlier server process; only its log remains.
            </p>
          )}
        </>
      )}

      <pre className="log" ref={pre} style={{ marginTop: 12 }}>
        {log ? <LogLines text={log} /> : job?.status === 'running' ? 'Starting…' : 'No output.'}
      </pre>

      {job?.status === 'running' && (
        <button className="secondary block" style={{ marginTop: 12 }} onClick={cancel}>
          Cancel
        </button>
      )}
    </Card>
  )
}

/** LogLines colours the runner's own lines: the commands it ran and how the
 *  job ended. Everything else is the study's output, as it wrote it. */
function LogLines({ text }: { text: string }) {
  const lines = text.split('\n')
  return (
    <>
      {lines.map((line, i) => {
        const cls = line.startsWith('$ ') ? 'cmd' : line === 'done' || line.startsWith('PROMOTED:') ? 'ok' : line.startsWith('failed:') || line.startsWith('Traceback') ? 'err' : ''
        return (
          <span key={i} className={cls}>
            {line}
            {i < lines.length - 1 ? '\n' : ''}
          </span>
        )
      })}
    </>
  )
}

function JobBadge({ status }: { status: JobStatus }) {
  switch (status) {
    case 'running':
      return (
        <Badge tone="accent" dot pulse>
          Running
        </Badge>
      )
    case 'succeeded':
      return <Badge tone="good">Done</Badge>
    case 'failed':
      return <Badge tone="bad">Failed</Badge>
    case 'cancelled':
      return <Badge tone="warn">Cancelled</Badge>
    default:
      return <Badge tone="neutral">Earlier</Badge>
  }
}
