import type { UIEvent } from './api'

export type ToolChip = {
  seq: number
  // turn is the turn_id of the reply that requested the call.
  turn: string
  name: string
  // pending is a call the model is still writing; waiting is one that needs
  // the user's approval; stopped is a call the user interrupted; unknown is
  // one a lost Worker left started, so whether it ran is not known.
  state: 'pending' | 'waiting' | 'running' | 'done' | 'failed' | 'stopped' | 'unknown'
  args?: unknown
  // startedAt is when the call was requested, in epoch milliseconds.
  startedAt: number
  durationMs?: number
  // result is the text of the call's result.
  result?: string
  // blob is set when the full result is too large to keep inline; result then
  // holds only its preview.
  blob?: { sha256: string; size: number }
  // error is the error text of a failed call, or the note of an interrupted one.
  error?: string
  // approvedBy is who allowed the call: the user, or the mode for a tool that needs no approval.
  approvedBy?: string
  // progress is the latest progress text of a running call.
  progress?: string
  // memory is set for a finished memory write.
  memory?: { id: string; version: number; op: string; path: string }
}

type Part = { text?: string; mem?: { id: string; v: number } }

type Payload = {
  tool_call_id: string
  turn_id?: string
  tool?: string
  ask?: boolean
  approved_by?: string
  args?: { command?: string; path?: string; new_path?: string }
  is_error?: boolean
  duration_ms?: number
  reason?: string
  note?: string
  result?: { parts: { tr?: { parts: Part[] } }[] }
  blob_ref?: { sha256: string; size: number }
  preview?: string
}

const memoryWrites = ['create', 'str_replace', 'insert', 'delete', 'rename']

// toolChips is one chip per tool call in the stream, in the order the calls
// were requested. progress is the live progress text by call id; a chip shows
// it only while its call runs.
export function toolChips(events: UIEvent[], progress: Record<string, string> = {}): ToolChip[] {
  const chips = new Map<string, ToolChip>()
  let turn = ''
  for (const e of events) {
    const p = e.payload as Payload
    switch (e.type) {
      case 'llm.response':
        turn = p.turn_id ?? String(e.seq)
        break
      case 'tool.call.requested':
        chips.set(p.tool_call_id, {
          seq: e.seq,
          turn,
          name: p.tool ?? '',
          state: p.ask ? 'waiting' : 'running',
          args: p.args,
          startedAt: Date.parse(e.created_at),
        })
        break
      case 'tool.call.started': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = 'running'
        chip.approvedBy = p.approved_by
        break
      }
      case 'tool.call.completed': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = p.is_error ? 'failed' : 'done'
        chip.durationMs = p.duration_ms
        chip.result = p.blob_ref ? p.preview : resultText(p)
        chip.blob = p.blob_ref
        if (p.is_error) chip.error = chip.result
        const a = chip.args as Payload['args']
        const mem = resultParts(p).find((x) => x.mem)?.mem
        if (!p.is_error && mem && a?.command && memoryWrites.includes(a.command)) {
          const path = a.command === 'rename' ? a.new_path : a.path
          chip.memory = { id: mem.id, version: mem.v, op: a.command, path: path ?? '' }
        }
        break
      }
      case 'tool.call.interrupted': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = p.reason === 'worker_lost' ? 'unknown' : 'stopped'
        chip.error = p.note
        break
      }
    }
  }
  for (const [id, chip] of chips) if (chip.state === 'running') chip.progress = progress[id]
  return [...chips.values()]
}

function resultParts(p: Payload): Part[] {
  return p.result?.parts[0]?.tr?.parts ?? []
}

function resultText(p: Payload): string {
  return resultParts(p)
    .map((x) => x.text ?? '')
    .join('')
}

// thinkingText is the reasoning text of an llm.response payload, or '' if it has none.
export function thinkingText(payload: unknown): string {
  const message = (payload as { message?: { parts: { k?: string; th?: { text?: string } }[] } }).message
  return (
    message?.parts
      .filter((p) => p.k === 'thinking')
      .map((p) => p.th?.text ?? '')
      .join('\n\n') ?? ''
  )
}

export type Source = { url: string; title: string }

// sources is the web pages the text of an llm.response payload cites, each
// once, in order. Only http(s) links are kept.
export function sources(payload: unknown): Source[] {
  const message = (payload as { message?: { parts: { cit?: { url: string; title?: string }[] }[] } }).message
  const seen = new Map<string, Source>()
  for (const part of message?.parts ?? []) {
    for (const c of part.cit ?? []) {
      if (/^https?:\/\//i.test(c.url) && !seen.has(c.url)) seen.set(c.url, { url: c.url, title: c.title || c.url })
    }
  }
  return [...seen.values()]
}

// textSegments is the text of each part of an llm.response payload with the
// 1-based numbers, in sources() order, of the pages that part cites.
export function textSegments(payload: unknown): { text: string; cites: number[] }[] {
  const message = (payload as { message?: { parts: { text?: string; cit?: { url: string }[] }[] } }).message
  const urls = sources(payload).map((s) => s.url)
  return (message?.parts ?? []).map((p) => ({
    text: p.text ?? '',
    cites: [...new Set((p.cit ?? []).map((c) => urls.indexOf(c.url) + 1).filter((n) => n > 0))],
  }))
}
