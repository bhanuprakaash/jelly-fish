import { useState } from 'react'
import { createConnector, HttpError, probeConnector, type Connector, type ConnectorEra, type ConnectorTool } from './lib/api'
import { useAction } from './lib/useAction'

type SwitchProps = { tool: ConnectorTool; disabled?: boolean; onToggle: () => void }

// ToolSwitch is one tool's on/off row.
export function ToolSwitch({ tool, disabled, onToggle }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={tool.enabled}
      aria-label={tool.name}
      disabled={disabled}
      onClick={onToggle}
      className="group flex min-h-11 w-full items-center gap-3 text-left focus-visible:outline-none disabled:opacity-60"
    >
      <span className="min-w-0 flex-1">
        <span className="block truncate font-mono text-sm">{tool.name}</span>
        <span className="line-clamp-2 text-sm text-muted">{tool.description}</span>
      </span>
      <span
        className={`flex h-6.5 w-11 shrink-0 rounded-full p-[3px] group-focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] ${
          tool.enabled ? 'justify-end bg-accent-ink' : 'justify-start bg-sunk ring-1 ring-ink2/60 ring-inset'
        }`}
      >
        <span className={`size-5 rounded-full ${tool.enabled ? 'bg-surface' : 'bg-muted'}`} />
      </span>
    </button>
  )
}

const slugOf = (name: string) => name.toLowerCase().replace(/[^a-z0-9]/g, '')

type Props = { onAdded: (c: Connector) => void }

// ConnectorForm adds a Connector: probe the server, pick its tools, save. The
// slug follows the name until it is edited, and cannot change after saving.
export function ConnectorForm({ onAdded }: Props) {
  const [name, setName] = useState('')
  const [slugEdit, setSlugEdit] = useState<string | null>(null)
  const [url, setUrl] = useState('')
  const [useKey, setUseKey] = useState(false)
  const [header, setHeader] = useState('')
  const [secret, setSecret] = useState('')
  const [probed, setProbed] = useState<{ era: ConnectorEra; tools: ConnectorTool[] } | null>(null)
  const { run, busy, error, setError } = useAction({ 409: 'That slug is taken' })

  const slug = slugEdit ?? slugOf(name)
  const server = { url, ...(useKey && secret ? { auth_header: header, secret } : {}) }
  const changed = (set: (v: string) => void) => (e: React.ChangeEvent<HTMLInputElement>) => {
    set(e.target.value)
    setProbed(null)
    setError(null)
  }

  const probe = () =>
    run(async () => {
      setProbed(null)
      try {
        const res = await probeConnector(server)
        setProbed({ era: res.era, tools: res.tools.map((t) => ({ ...t, enabled: true })) })
      } catch (err) {
        if (!(err instanceof HttpError) || (err.status !== 422 && err.status !== 502)) throw err
        setError(err.detail)
      }
    })

  const save = () =>
    run(async () => {
      if (!probed) return
      onAdded(
        await createConnector({
          ...server,
          name,
          slug: slugEdit ?? undefined,
          era: probed.era,
          tools: probed.tools.map((t) => ({ name: t.name, enabled: t.enabled })),
        }),
      )
    })

  const toggle = (tool: ConnectorTool) =>
    setProbed((p) => p && { ...p, tools: p.tools.map((t) => (t === tool ? { ...t, enabled: !t.enabled } : t)) })

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (probed) save()
        else probe()
      }}
      className="space-y-3 rounded-card border border-line bg-surface p-4"
    >
      <div className="flex flex-wrap gap-3">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-label="Name"
          placeholder="Name"
          autoComplete="off"
          className="input h-11 min-w-0 flex-1 basis-48"
        />
        <input
          value={slug}
          onChange={(e) => {
            setSlugEdit(slugOf(e.target.value))
            setError(null)
          }}
          aria-label="Slug"
          placeholder="slug"
          autoComplete="off"
          className="input h-11 min-w-0 flex-1 basis-32 font-mono"
        />
      </div>
      <input
        type="url"
        value={url}
        onChange={changed(setUrl)}
        aria-label="URL"
        placeholder="https://example.com/mcp"
        autoComplete="off"
        className="input h-11 w-full"
      />
      <button
        type="button"
        role="switch"
        aria-checked={useKey}
        onClick={() => setUseKey(!useKey)}
        className="group flex min-h-11 w-fit items-center gap-2.5 text-sm text-ink2 focus-visible:outline-none"
      >
        Uses an API key
        <span
          className={`flex h-6.5 w-11 rounded-full p-[3px] group-focus-visible:shadow-[0_0_0_2px_var(--bg),0_0_0_4px_var(--accent-ink)] ${
            useKey ? 'justify-end bg-accent-ink' : 'justify-start bg-sunk ring-1 ring-ink2/60 ring-inset'
          }`}
        >
          <span className={`size-5 rounded-full ${useKey ? 'bg-surface' : 'bg-muted'}`} />
        </span>
      </button>
      {useKey && (
        <div className="flex flex-wrap gap-3">
          <input
            value={header}
            onChange={changed(setHeader)}
            aria-label="Header name"
            placeholder="Header name"
            autoComplete="off"
            className="input h-11 min-w-0 flex-1 basis-40 font-mono"
          />
          <input
            type="password"
            value={secret}
            onChange={changed(setSecret)}
            aria-label="Secret"
            placeholder="Secret"
            autoComplete="off"
            className="input h-11 min-w-0 flex-1 basis-40"
          />
        </div>
      )}
      {probed && (
        <div className="space-y-1 border-t border-line pt-3">
          <p className="text-sm font-semibold">
            {probed.tools.length} {probed.tools.length === 1 ? 'tool' : 'tools'} found
          </p>
          {probed.tools.map((t) => (
            <ToolSwitch key={t.name} tool={t} onToggle={() => toggle(t)} />
          ))}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {probed ? (
        <button type="submit" disabled={busy || name.trim() === ''} className="btn btn-primary h-11">
          Save
        </button>
      ) : (
        <button type="submit" disabled={busy || url.trim() === ''} className="btn btn-secondary h-11">
          Probe
        </button>
      )}
    </form>
  )
}
