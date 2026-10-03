import { useEffect, useRef, useState, type SubmitEvent } from 'react'
import { HttpError, UnauthorizedError } from './lib/api'

type MenuProps = {
  // renaming is true while the row shows its rename form instead of the link.
  renaming: boolean
  onRename: () => void
}

// RowMenu is the "⋯" button of a Chat List row and its menu.
export function RowMenu({ renaming, onRename }: MenuProps) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setOpen(false)
      button.current?.focus()
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  // Leaving the rename form, saved or cancelled, puts focus back on the button.
  const wasRenaming = useRef(false)
  useEffect(() => {
    if (wasRenaming.current && !renaming) button.current?.focus()
    wasRenaming.current = renaming
  }, [renaming])

  return (
    <div ref={root} className={renaming ? 'hidden' : 'relative shrink-0'}>
      <button
        ref={button}
        type="button"
        aria-label="Chat options"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="rounded-lg px-2 py-1 text-neutral-400 hover:text-neutral-100"
      >
        ⋯
      </button>
      {open && (
        <div
          role="menu"
          className="absolute top-full right-0 z-10 mt-1 w-32 rounded-lg border border-neutral-800 bg-neutral-900 py-1 shadow-lg"
        >
          <button
            type="button"
            role="menuitem"
            autoFocus
            onClick={() => {
              setOpen(false)
              onRename()
            }}
            className="w-full px-3 py-1.5 text-left text-sm hover:bg-neutral-800"
          >
            Rename
          </button>
        </div>
      )}
    </div>
  )
}

type FormProps = {
  initial: string
  // onSave rejects when the server refuses the title.
  onSave: (title: string) => Promise<void>
  onCancel: () => void
  onUnauthorized: () => void
}

// RenameForm replaces a row's link while its chat is being renamed.
export function RenameForm({ initial, onSave, onCancel, onUnauthorized }: FormProps) {
  const [value, setValue] = useState(initial)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const input = useRef<HTMLInputElement>(null)
  const title = value.trim()

  useEffect(() => {
    input.current?.select()
  }, [])

  const submit = async (e: SubmitEvent) => {
    e.preventDefault()
    if (!title || saving) return
    setSaving(true)
    setError(null)
    try {
      await onSave(title)
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        onUnauthorized()
        return
      }
      const refused = err instanceof HttpError && (err.status === 400 || err.status === 422)
      setError(refused ? 'Title must be 1–100 characters' : 'Could not rename. Try again.')
      setSaving(false)
    }
  }

  return (
    <form
      onSubmit={submit}
      onKeyDown={(e) => {
        if (e.key === 'Escape') onCancel()
      }}
      className="min-w-0 flex-1 px-2 py-1"
    >
      <input
        ref={input}
        aria-label="Title"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        autoFocus
        className="w-full rounded-lg bg-neutral-900 px-2 py-1 text-sm outline-none"
      />
      <div className="mt-1 flex gap-2">
        <button
          type="submit"
          disabled={!title || saving}
          className="rounded-lg bg-neutral-100 px-3 py-1 text-xs font-medium text-neutral-900 disabled:opacity-50"
        >
          Save
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="rounded-lg px-3 py-1 text-xs text-neutral-400 hover:text-neutral-200"
        >
          Cancel
        </button>
      </div>
      {error && (
        <p role="alert" className="mt-1 text-xs text-red-400">
          {error}
        </p>
      )}
    </form>
  )
}
