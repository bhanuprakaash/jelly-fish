import { useState } from 'react'
import { HttpError, undoMemory, UnauthorizedError, type Memory } from './lib/api'
import type { ToolChip } from './lib/toolChips'
import { MemoryActions } from './MemoryActions'

type Props = {
  chip: ToolChip & { memory: NonNullable<ToolChip['memory']> }
  // memories is every memory the User has, or null until it has loaded.
  memories: Memory[] | null
  onChanged: () => void
  onUnauthorized: () => void
}

const labels: Record<string, string> = {
  create: 'Remembered',
  str_replace: 'Updated memory',
  insert: 'Updated memory',
  rename: 'Updated memory',
  delete: 'Forgot',
}

// MemoryChip is the chat chip of one memory write: [Undo] while the memory is
// still as the write left it (for a delete, while it is absent from the list),
// and [Keep] [Edit] [Delete] while it waits for review.
export function MemoryChip({ chip, memories, onChanged, onUnauthorized }: Props) {
  const [error, setError] = useState<string | null>(null)
  const { id, version, op, path } = chip.memory
  const current = memories?.find((m) => m.id === id)

  const undo = async () => {
    setError(null)
    try {
      await undoMemory(id, version)
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      const changed = err instanceof HttpError && err.status === 409
      setError(changed ? 'Changed since, so it can’t be undone.' : 'Could not undo. Try again.')
    }
    onChanged()
  }

  const pending = current?.status === 'pending_review'
  const canUndo =
    (current?.status === 'active' && current.version === version) || (op === 'delete' && memories && !current)

  return (
    <div className="mr-auto w-full space-y-1 text-sm">
      {pending ? (
        <div className="space-y-2 rounded-card border border-attention-line bg-surface px-4 py-3">
          <p className="text-ink2">
            Wants to remember <span className="font-mono">{path}</span>: <em>“{current.content}”</em>
          </p>
          <MemoryActions key={current.version} memory={current} onChanged={onChanged} onUnauthorized={onUnauthorized} />
        </div>
      ) : (
        <div className="flex w-fit max-w-full items-center gap-2 rounded-full border border-accent-line bg-accent-tint py-1 pl-3 pr-1 text-ink2">
          <span className="min-w-0 break-words">
            {memories ? labels[op] : 'Memory'} · {current?.title || path}
            {memories && !current && ' · removed'}
          </span>
          {canUndo && (
            <button
              onClick={undo}
              className="h-[26px] shrink-0 cursor-pointer rounded-full bg-surface px-2.5 text-xs font-semibold text-ink focus-visible:outline-none focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)]"
            >
              Undo
            </button>
          )}
        </div>
      )}
      {error && <p className="text-danger">{error}</p>}
    </div>
  )
}
