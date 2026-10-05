import { useState } from 'react'
import {
  approveMemory,
  deleteMemory,
  editMemory,
  HttpError,
  UnauthorizedError,
  type Memory,
} from './lib/api'

type Props = {
  memory: Memory
  // onChanged is called after any change, so the caller loads the memories again.
  onChanged: () => void
  onUnauthorized: () => void
}

// MemoryActions is [Approve] [Edit] [Delete] for one memory; Approve shows
// only while the memory waits for review. Editing a pending memory approves it.
export function MemoryActions({ memory, onChanged, onUnauthorized }: Props) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(memory.content)
  const [error, setError] = useState<string | null>(null)

  const run = async (action: () => Promise<void>) => {
    setError(null)
    try {
      await action()
      setEditing(false)
      onChanged()
    } catch (err) {
      if (err instanceof UnauthorizedError) onUnauthorized()
      else if (err instanceof HttpError && err.status === 422) {
        setError('Not saved: the text is empty, over 4 KB, or looks like a secret.')
      } else if (err instanceof HttpError && err.status === 409) {
        setError('Changed since, so it can’t be saved or approved. Reload to see the latest.')
      } else setError('Something went wrong. Try again.')
    }
  }

  return (
    <div className="space-y-2">
      {editing && (
        <div className="space-y-2">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            rows={6}
            aria-label="Memory text"
            className="input w-full px-3 py-2 text-sm"
          />
          <div className="flex gap-2">
            <button className="btn btn-primary btn-sm" onClick={() => run(() => editMemory(memory.id, draft, memory.version))}>
              Save
            </button>
            <button className="btn btn-ghost btn-sm" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {!editing && (
        <div className="flex gap-2">
          {memory.status === 'pending_review' && (
            <button className="btn btn-primary btn-sm" onClick={() => run(() => approveMemory(memory.id, memory.version))}>
              Approve
            </button>
          )}
          <button
            className="btn btn-secondary btn-sm"
            onClick={() => {
              setDraft(memory.content)
              setEditing(true)
            }}
          >
            Edit
          </button>
          <button
            className="btn btn-danger btn-sm"
            onClick={() => {
              if (window.confirm('Delete this memory and its history? This cannot be undone.')) {
                void run(() => deleteMemory(memory.id))
              }
            }}
          >
            Delete
          </button>
        </div>
      )}
      {error && <p className="text-sm text-danger">{error}</p>}
    </div>
  )
}
