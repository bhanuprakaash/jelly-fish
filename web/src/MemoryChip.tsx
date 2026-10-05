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

// MemoryChip is the chat chip of one memory write: [Undo] while the memory is
// still as the write left it (for a delete, while it is absent from the list),
// and [Approve] [Edit] [Delete] while it waits for review.
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

  return (
    <div className="mr-auto max-w-md space-y-2 rounded-2xl bg-neutral-900 px-3 py-1 text-sm text-neutral-400">
      <p>
        memory · {op} {path}
        {memories && !current && ' · removed'}
        {current?.status === 'pending_review' && ' · pending review'}
      </p>
      {current?.status === 'pending_review' && (
        <MemoryActions key={current.version} memory={current} onChanged={onChanged} onUnauthorized={onUnauthorized} />
      )}
      {((current?.status === 'active' && current.version === version) || (op === 'delete' && memories && !current)) && (
        <button onClick={undo} className="rounded-lg bg-neutral-800 px-3 py-1 text-neutral-200 hover:bg-neutral-700">
          Undo
        </button>
      )}
      {error && <p className="text-red-400">{error}</p>}
    </div>
  )
}
