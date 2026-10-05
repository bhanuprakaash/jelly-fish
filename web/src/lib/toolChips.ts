import type { UIEvent } from './api'

export type ToolChip = {
  seq: number
  name: string
  // stopped is a call the user interrupted; unknown is one a lost Worker left
  // started, so whether it ran is not known.
  state: 'running' | 'done' | 'failed' | 'stopped' | 'unknown'
  args?: unknown
  durationMs?: number
  // result is the text of the call's result.
  result?: string
  // error is the error text of a failed call, or the note of an interrupted one.
  error?: string
  // memory is set for a finished memory write.
  memory?: { id: string; version: number; op: string; path: string }
}

type Part = { text?: string; mem?: { id: string; v: number } }

type Payload = {
  tool_call_id: string
  tool?: string
  args?: { command?: string; path?: string; new_path?: string }
  is_error?: boolean
  duration_ms?: number
  reason?: string
  note?: string
  result?: { parts: { tr?: { parts: Part[] } }[] }
}

const memoryWrites = ['create', 'str_replace', 'insert', 'delete', 'rename']

// toolChips is one chip per tool call in the stream, in the order the calls
// were requested.
export function toolChips(events: UIEvent[]): ToolChip[] {
  const chips = new Map<string, ToolChip>()
  const args = new Map<string, Payload['args']>()
  for (const e of events) {
    const p = e.payload as Payload
    switch (e.type) {
      case 'tool.call.requested':
        chips.set(p.tool_call_id, { seq: e.seq, name: p.tool ?? '', state: 'running', args: p.args })
        args.set(p.tool_call_id, p.args)
        break
      case 'tool.call.completed': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = p.is_error ? 'failed' : 'done'
        chip.durationMs = p.duration_ms
        chip.result = resultText(p)
        if (p.is_error) chip.error = chip.result
        const a = args.get(p.tool_call_id)
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
