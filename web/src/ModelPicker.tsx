import type { ModelOption, Models } from './lib/api'

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

// optionLabel puts token limits first; dollars are only an estimate
// (provider-gateway.md §8).
function optionLabel(m: ModelOption): string {
  const parts = [m.display_name || m.id]
  if (m.context_window) parts.push(`${tokens(m.context_window)} tokens`)
  parts.push(m.price ? `≈ $${m.price.input} / $${m.price.output} per M` : 'price unknown')
  return parts.join(' · ')
}

// providerOf names the Provider of the group listing model.
function providerOf(models: Models, model: string): string | undefined {
  return models.providers.find((g) => g.models.some((m) => m.id === model))?.provider
}

type Props = {
  models: Models
  current: string
  onPick: (model: string) => void
  disabled?: boolean
}

// ModelPicker lists models per Provider. A Provider with no key is greyed
// out. Only the current Provider's models can be picked, since history
// isn't replayed across Providers (#5); the Fake Provider, which has no
// history of its own, is exempt either way.
export function ModelPicker({ models, current, onPick, disabled }: Props) {
  const from = providerOf(models, current)
  const pickable = (provider: string) =>
    !from || provider === from || provider === 'fake' || from === 'fake'

  return (
    <select
      aria-label="Model"
      value={current}
      disabled={disabled}
      onChange={(e) => onPick(e.target.value)}
      className="max-w-[60vw] truncate rounded-lg bg-neutral-900 px-2 py-1 text-sm text-neutral-200 disabled:opacity-50"
    >
      {!from && <option value={current}>{current}</option>}
      {models.providers.map((g) => (
        <optgroup
          key={g.provider}
          label={g.available ? providerName(g.provider) : `${providerName(g.provider)} · Add ${providerName(g.provider)} key`}
        >
          {g.models.map((m) => (
            <option key={m.id} value={m.id} disabled={!g.available || !pickable(g.provider)}>
              {optionLabel(m)}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  )
}
