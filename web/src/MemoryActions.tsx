import { useState } from 'react'
import {
  approveMemory,
  deleteMemory,
  editMemory,
  type Memory,
} from './lib/api'
import { useAction } from './lib/useAction'

type Props = {
  memory: Memory
  // onChanged is called after any change, so the caller loads the memories again.
  onChanged: () => void
}

// MemoryActions is [Keep] [Edit] [Delete] for one memory; Keep shows
// only while the memory waits for review. Editing a pending memory approves it.
export function MemoryActions({ memory, onChanged }: Props) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(memory.content)
  const { run: runAction, error } = useAction({
    422: 'Not saved: the text is empty, over 4 KB, or looks like a secret.',
    409: 'Changed since, so it can’t be saved or approved. Reload to see the latest.',
  })

  const run = (action: () => Promise<void>) =>
    runAction(async () => {
      await action()
      setEditing(false)
      onChanged()
    })

  return (
    <div className="space-y-2">
      {editing && (
        <div className="space-y-2">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            rows={6}
            aria-label="Memory text"
            className="input h-auto w-full px-3 py-2 text-sm"
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
              Keep
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
