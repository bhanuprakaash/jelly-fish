import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type Ref } from 'react'
import type { ModelOption, Models } from './lib/api'
import { providerOf } from './lib/sessionModel'

const providerNames: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  gemini: 'Gemini',
  fake: 'Fake',
}

const providerName = (p: string) => providerNames[p] ?? p

function tokens(n: number): string {
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${Math.round(n / 1_000)}K`
  return String(n)
}

// optionMeta puts token limits first; dollars are only an estimate
// (provider-gateway.md §8).
function optionMeta(m: ModelOption): string {
  const parts: string[] = []
  if (m.context_window) parts.push(`${tokens(m.context_window)} tokens`)
  parts.push(m.price ? `$${m.price.input}/$${m.price.output}` : 'price unknown')
  return parts.join(' · ')
}

// missing names the features the model lacks; a model the picker knows
// nothing about names none.
function missing(m: ModelOption): string[] {
  const labels: string[] = []
  if (m.web_search === false) labels.push('no search')
  if (m.thinking === false) labels.push('no thinking')
  return labels
}

type Item = { m: ModelOption; enabled: boolean }

type Props = {
  models: Models
  current: string
  onPick: (model: string) => void
  disabled?: boolean
  ref?: Ref<HTMLButtonElement>
}

// ModelPicker lists models per Provider in a popover, a bottom sheet on phones.
// A Provider with no key is disabled, with a link to add one. Focus stays on the
// listbox; the highlighted option is its aria-activedescendant.
export function ModelPicker({ models, current, onPick, disabled, ref }: Props) {
  const from = providerOf(models, current)

  const option = models.providers.flatMap((g) => g.models).find((m) => m.id === current)
  const name = option?.display_name || current

  const items: Item[] = from ? [] : [{ m: { id: current, price: null }, enabled: true }]
  const orphans = items.length
  const groups = models.providers.map((g) => ({
    ...g,
    items: g.models.map((m): Item => {
      const item: Item = { m, enabled: g.available }
      items.push(item)
      return item
    }),
  }))

  const uid = useId()
  const optionId = (i: number) => `${uid}-opt-${i}`
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [up, setUp] = useState(false)
  const root = useRef<HTMLSpanElement>(null)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const list = useRef<HTMLDivElement>(null)
  const picked = useRef(false)

  // step finds the next enabled item from start in dir, wrapping.
  const step = (start: number, dir: 1 | -1) => {
    for (let k = 1; k <= items.length; k++) {
      const i = (start + dir * k + items.length * 2) % items.length
      if (items[i].enabled) return i
    }
    return -1
  }
  const openMenu = () => {
    const i = items.findIndex((it) => it.m.id === current)
    setActive(i >= 0 && items[i].enabled ? i : step(-1, 1))
    setOpen(true)
  }
  const close = () => {
    setOpen(false)
    trigger.current?.focus()
  }
  const pick = (i: number) => {
    if (!items[i]?.enabled) return
    close()
    if (items[i].m.id === current) return
    picked.current = true
    onPick(items[i].m.id)
  }

  // The parent disables the trigger while a pick is saved, which drops focus.
  useEffect(() => {
    if (disabled || !picked.current) return
    picked.current = false
    if (document.activeElement === document.body) trigger.current?.focus()
  }, [disabled])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  // Flip above the trigger when the popover doesn't fit below it.
  useLayoutEffect(() => {
    if (!open || !list.current || !trigger.current) return
    list.current.focus({ preventScroll: true })
    const r = trigger.current.getBoundingClientRect()
    const below = window.innerHeight - r.bottom
    setUp(below < list.current.offsetHeight + 8 && r.top > below)
  }, [open])

  useLayoutEffect(() => {
    if (open && active >= 0) document.getElementById(optionId(active))?.scrollIntoView({ block: 'nearest' })
  })

  const onListKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      setActive(step(active, e.key === 'ArrowDown' ? 1 : -1))
    } else if (e.key === 'Home') {
      setActive(step(-1, 1))
    } else if (e.key === 'End') {
      setActive(step(items.length, -1))
    } else if (e.key === 'Enter' || e.key === ' ') {
      pick(active)
    } else if (e.key === 'Tab') {
      close()
      return
    } else {
      return
    }
    e.preventDefault()
  }

  const row = (item: Item) => {
    const i = items.indexOf(item)
    const isCurrent = item.m.id === current
    return (
      <div
        key={item.m.id}
        id={optionId(i)}
        role="option"
        aria-selected={isCurrent}
        aria-disabled={!item.enabled}
        onMouseMove={() => item.enabled && setActive(i)}
        onClick={() => pick(i)}
        className={`flex min-h-11 scroll-mt-8 items-center gap-3 rounded-btn px-2.5 py-2 text-sm md:min-h-9 md:py-1.5 ${
          item.enabled ? 'cursor-pointer text-ink' : 'cursor-not-allowed text-muted'
        } ${isCurrent ? 'font-semibold' : ''} ${i === active ? 'bg-accent-tint' : ''}`}
      >
        <span className="min-w-0 flex-1 truncate">{item.m.display_name || item.m.id}</span>
        {missing(item.m).map((label) => (
          <span key={label} className="shrink-0 rounded-full bg-sunk px-1.5 text-[11px] text-muted">
            {label}
          </span>
        ))}
        <span
          title="Estimated USD per million input/output tokens"
          className="shrink-0 text-xs font-normal text-muted tabular-nums"
        >
          {optionMeta(item.m)}
        </span>
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.5"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
          className={`shrink-0 ${isCurrent ? '' : 'invisible'}`}
        >
          <path d="M5 12.5l4.5 4.5L19 7" />
        </svg>
      </div>
    )
  }

  return (
    <span
      ref={root}
      onKeyDown={(e) => {
        if (open && e.key === 'Escape') {
          e.preventDefault()
          close()
        }
      }}
      className="relative inline-flex max-w-full"
    >
      <button
        ref={(el) => {
          trigger.current = el
          if (typeof ref === 'function') ref(el)
          else if (ref) ref.current = el
        }}
        type="button"
        aria-label={`Model: ${name}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => (open ? close() : openMenu())}
        onKeyDown={(e) => {
          if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
          e.preventDefault()
          openMenu()
        }}
        className="inline-flex max-w-full items-center gap-1 rounded-btn font-mono text-sm text-ink2 hover:text-ink focus-ring disabled:cursor-not-allowed disabled:opacity-50"
      >
        <span className="truncate">{name}</span>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" aria-hidden="true" className="shrink-0">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>
      {open && (
        <>
          <div aria-hidden="true" onClick={close} className="fixed inset-0 z-40 bg-black/40 md:hidden" />
          <div
            ref={list}
            role="listbox"
            tabIndex={0}
            aria-label="Model"
            aria-activedescendant={active >= 0 ? optionId(active) : undefined}
            onKeyDown={onListKeyDown}
            className={`fixed inset-x-2 bottom-2 z-50 max-h-[70vh] overflow-y-auto rounded-card border border-line bg-surface p-1 font-sans shadow-[0_8px_24px_rgb(0_0_0/0.16)] outline-none md:absolute md:inset-x-auto md:bottom-auto md:left-0 md:max-h-[60vh] md:w-[400px] md:max-w-[calc(100vw-1rem)] ${
              up ? 'md:top-auto md:bottom-full md:mb-1' : 'md:top-full md:mt-1'
            }`}
          >
            {items.slice(0, orphans).map(row)}
            {groups.map((g) => {
              const label = providerName(g.provider)
              return (
                <div key={g.provider} role="group" aria-labelledby={`${uid}-grp-${g.provider}`}>
                  <div className="flex items-center justify-between px-2.5 pt-2 pb-1 text-[11px] font-medium tracking-[0.14em] text-muted uppercase">
                    <span id={`${uid}-grp-${g.provider}`}>{label}</span>
                    {!g.available && (
                      <a
                        href="/settings"
                        aria-label={`Add ${label} key`}
                        className="rounded-sm text-xs font-semibold tracking-normal text-accent-ink normal-case underline-offset-2 hover:underline focus-ring"
                      >
                        Add key
                      </a>
                    )}
                  </div>
                  {g.items.map(row)}
                </div>
              )
            })}
          </div>
        </>
      )}
    </span>
  )
}
