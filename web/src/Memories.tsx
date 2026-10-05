import { useCallback, useEffect, useState } from 'react'
import {
  getMemories,
  getMemoryRevisions,
  setUseUserMemory,
  UnauthorizedError,
  type Memory,
  type MemoryProject,
  type MemoryRevision,
} from './lib/api'
import { MemoryActions } from './MemoryActions'

type Props = {
  onUnauthorized: () => void
}

// Memories is the page of what the assistant remembers: "About me" (User
// Memory) and Project Memory, each entry with its history and actions.
export function Memories({ onUnauthorized }: Props) {
  const [data, setData] = useState<{ project: MemoryProject; memories: Memory[] } | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    getMemories().then(setData, (err) => {
      if (err instanceof UnauthorizedError) onUnauthorized()
      else setError('Could not load memories.')
    })
  }, [onUnauthorized])

  useEffect(load, [load])

  const toggle = async (use: boolean) => {
    if (!data) return
    setError(null)
    try {
      await setUseUserMemory(data.project.id, use)
      load()
    } catch (err) {
      if (err instanceof UnauthorizedError) onUnauthorized()
      else setError('Could not save the setting. Try again.')
    }
  }

  const column = (scope: Memory['scope']) => data?.memories.filter((m) => m.scope === scope) ?? []

  return (
    <div className="min-h-svh bg-bg text-ink">
      <header className="border-b border-line px-4 py-3">
        <a href="/" className="text-sm text-muted hover:text-ink">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-5xl space-y-6 px-4 py-6 sm:px-8 sm:py-10">
        <div className="flex flex-wrap items-end gap-4">
          <div className="min-w-0 flex-1 basis-72">
            <h1 className="text-3xl font-semibold tracking-[-0.035em] sm:text-4xl">Memories</h1>
            <p className="mt-1 text-muted">Notes the assistant keeps across chats. You can read, edit or delete every one.</p>
          </div>
          {data && (
            <button
              type="button"
              role="switch"
              aria-checked={data.project.use_user_memory}
              onClick={() => toggle(!data.project.use_user_memory)}
              className="inline-flex min-h-10 items-center gap-2.5 text-sm text-ink2"
            >
              Use what I&apos;ve told you about me
              <span
                className={`flex h-6.5 w-11 rounded-full p-[3px] ${
                  data.project.use_user_memory ? 'justify-end bg-accent-ink' : 'justify-start bg-line2'
                }`}
              >
                <span className="size-5 rounded-full bg-surface" />
              </span>
            </button>
          )}
        </div>
        {error && <p className="text-sm text-danger">{error}</p>}
        {!data && !error && <p className="text-muted">Loading…</p>}
        {data && (
          <div className="grid gap-7 md:grid-cols-2">
            <MemoryColumn
              title="About me"
              memories={column('user')}
              onChanged={load}
              onUnauthorized={onUnauthorized}
            />
            <MemoryColumn
              title="This project"
              subtitle={data.project.name}
              memories={column('project')}
              onChanged={load}
              onUnauthorized={onUnauthorized}
            />
          </div>
        )}
      </main>
    </div>
  )
}

type ListProps = {
  memories: Memory[]
  onChanged: () => void
  onUnauthorized: () => void
}

function MemoryColumn({ title, subtitle, memories, onChanged, onUnauthorized }: ListProps & { title: string; subtitle?: string }) {
  return (
    <section className="min-w-0 space-y-3">
      <h2 className="flex items-baseline gap-2.5 border-b border-ink pb-2.5">
        <span className="text-lg font-semibold tracking-[-0.02em]">{title}</span>
        <span className="text-xs font-normal text-muted">
          {memories.length} {memories.length === 1 ? 'memory' : 'memories'}
          {subtitle && ` · ${subtitle}`}
        </span>
      </h2>
      {memories.length === 0 ? (
        <p className="text-sm text-muted">Nothing remembered yet.</p>
      ) : (
        <ul className="space-y-3">
          {memories.map((m) => (
            <MemoryCard key={m.id} memory={m} onChanged={onChanged} onUnauthorized={onUnauthorized} />
          ))}
        </ul>
      )}
    </section>
  )
}

function MemoryCard({ memory, onChanged, onUnauthorized }: { memory: Memory } & Omit<ListProps, 'memories'>) {
  const [history, setHistory] = useState<MemoryRevision[] | null>(null)
  const [historyError, setHistoryError] = useState(false)

  const showHistory = async () => {
    if (history) {
      setHistory(null)
      return
    }
    setHistoryError(false)
    try {
      setHistory(await getMemoryRevisions(memory.id))
    } catch (err) {
      if (err instanceof UnauthorizedError) onUnauthorized()
      else setHistoryError(true)
    }
  }

  return (
    <li
      className={`space-y-2 rounded-card border bg-surface px-4 py-4 ${
        memory.status === 'pending_review' ? 'border-attention-line' : 'border-line'
      } ${memory.stale ? 'opacity-70' : ''}`}
    >
      <div className="flex items-center gap-2">
        <span className="text-[11px] tracking-[0.08em] text-muted uppercase">{memory.kind}</span>
        <div className="ml-auto flex gap-1.5">
          {memory.status === 'pending_review' && (
            <span className="rounded-full bg-attention-tint px-2 py-0.5 text-[11px] font-medium text-attention">
              Pending review
            </span>
          )}
          {memory.stale && (
            <span className="rounded-full bg-sunk px-2 py-0.5 text-[11px] font-medium text-muted">Stale</span>
          )}
        </div>
      </div>
      <h3 className="font-semibold">{memory.title}</h3>
      <p className="text-sm whitespace-pre-wrap text-ink2">{memory.content}</p>
      <p className="text-xs text-muted">
        {memory.path} · updated {new Date(memory.updated_at).toLocaleString()}
        {history && ` · last by ${writers[history[history.length - 1].written_by]}`}
        {memory.source_session_id && (
          <>
            {' · '}
            <a href={`/s/${memory.source_session_id}`} className="underline hover:text-ink2">
              source chat
            </a>
          </>
        )}
      </p>
      <MemoryActions
        key={memory.version}
        memory={memory}
        onChanged={() => {
          setHistory(null)
          onChanged()
        }}
        onUnauthorized={onUnauthorized}
      />
      <button onClick={showHistory} className="btn btn-ghost btn-sm">
        {history ? 'Hide history' : 'History'}
      </button>
      {historyError && <p className="text-sm text-danger">Could not load history.</p>}
      {history && <History revisions={history} />}
    </li>
  )
}

const writers = { agent: 'Assistant', user: 'You', tidy: 'Tidy' }

function History({ revisions }: { revisions: MemoryRevision[] }) {
  return (
    <ol className="space-y-2 border-l border-line pl-3 text-sm">
      {revisions.map((r, i) => (
        <li key={r.version}>
          <p className="text-muted">
            v{r.version} · {writers[r.written_by]} · {new Date(r.created_at).toLocaleString()} ·{' '}
            {changes(revisions[i - 1], r)}
          </p>
          <details>
            <summary className="cursor-pointer text-muted">Text at this version</summary>
            <p className="whitespace-pre-wrap text-ink2">{r.content}</p>
          </details>
        </li>
      ))}
    </ol>
  )
}

// changes names what a revision changed from the one before it.
function changes(prev: MemoryRevision | undefined, r: MemoryRevision): string {
  if (!prev) return 'created'
  const changed = [
    r.path !== prev.path && 'path',
    r.title !== prev.title && 'title',
    r.content !== prev.content && 'text',
  ].filter(Boolean)
  return changed.length ? `changed ${changed.join(', ')}` : 'approved'
}
