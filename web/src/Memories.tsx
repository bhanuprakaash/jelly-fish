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

  return (
    <div className="min-h-svh bg-neutral-950 text-neutral-100">
      <header className="border-b border-neutral-800 px-4 py-3">
        <a href="/" className="text-sm text-neutral-400 hover:text-neutral-200">
          ← Back
        </a>
      </header>
      <main className="mx-auto max-w-2xl space-y-8 px-4 py-6">
        <h1 className="text-2xl font-semibold">Memories</h1>
        {error && <p className="text-sm text-red-400">{error}</p>}
        {!data && !error && <p className="text-neutral-500">Loading…</p>}
        {data && (
          <>
            <section className="space-y-3">
              <h2 className="text-lg font-medium">About me</h2>
              <MemoryList
                memories={data.memories.filter((m) => m.scope === 'user')}
                onChanged={load}
                onUnauthorized={onUnauthorized}
              />
            </section>
            <section className="space-y-3">
              <h2 className="text-lg font-medium">Project Memory: {data.project.name}</h2>
              <label className="flex items-center gap-2 text-sm text-neutral-300">
                <input
                  type="checkbox"
                  checked={data.project.use_user_memory}
                  onChange={(e) => toggle(e.target.checked)}
                />
                Use what I&apos;ve told you about me
              </label>
              <MemoryList
                memories={data.memories.filter((m) => m.scope === 'project')}
                onChanged={load}
                onUnauthorized={onUnauthorized}
              />
            </section>
          </>
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

function MemoryList({ memories, onChanged, onUnauthorized }: ListProps) {
  if (memories.length === 0) return <p className="text-sm text-neutral-500">Nothing remembered yet.</p>
  return (
    <ul className="space-y-3">
      {memories.map((m) => (
        <MemoryCard key={m.id} memory={m} onChanged={onChanged} onUnauthorized={onUnauthorized} />
      ))}
    </ul>
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
    <li className="space-y-2 rounded-xl border border-neutral-800 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-medium">{memory.title}</h3>
        {memory.status === 'pending_review' && (
          <span className="rounded-full bg-amber-400/20 px-2 py-0.5 text-xs text-amber-300">Pending review</span>
        )}
        {memory.stale && (
          <span className="rounded-full bg-neutral-800 px-2 py-0.5 text-xs text-neutral-400">Stale</span>
        )}
      </div>
      <p className="text-xs text-neutral-500">
        {memory.path} · updated {new Date(memory.updated_at).toLocaleString()}
        {memory.source_session_id && (
          <>
            {' · '}
            <a href={`/s/${memory.source_session_id}`} className="underline hover:text-neutral-300">
              source chat
            </a>
          </>
        )}
      </p>
      <p className="text-sm whitespace-pre-wrap text-neutral-300">{memory.content}</p>
      <MemoryActions
        key={memory.version}
        memory={memory}
        onChanged={() => {
          setHistory(null)
          onChanged()
        }}
        onUnauthorized={onUnauthorized}
      />
      <button onClick={showHistory} className="text-sm text-neutral-400 hover:text-neutral-200">
        {history ? 'Hide history' : 'History'}
      </button>
      {historyError && <p className="text-sm text-red-400">Could not load history.</p>}
      {history && <History revisions={history} />}
    </li>
  )
}

const writers = { agent: 'Assistant', user: 'You', tidy: 'Tidy' }

function History({ revisions }: { revisions: MemoryRevision[] }) {
  return (
    <ol className="space-y-2 border-l border-neutral-800 pl-3 text-sm">
      {revisions.map((r, i) => (
        <li key={r.version}>
          <p className="text-neutral-400">
            v{r.version} · {writers[r.written_by]} · {new Date(r.created_at).toLocaleString()} ·{' '}
            {changes(revisions[i - 1], r)}
          </p>
          <details>
            <summary className="cursor-pointer text-neutral-500">Text at this version</summary>
            <p className="whitespace-pre-wrap text-neutral-300">{r.content}</p>
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
