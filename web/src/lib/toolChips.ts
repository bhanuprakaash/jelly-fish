import type { UIEvent } from './api'

export type ToolChip = {
  seq: number
  name: string
  state: 'running' | 'done' | 'failed' | 'interrupted'
  // error is the error text of a failed or interrupted call.
  error?: string
  // memory is set for a finished memory write that left a memory behind; a
  // delete leaves none.
  memory?: { id: string; version: number; op: string; path: string }
}

type Part = { text?: string; mem?: { id: string; v: number } }

type Payload = {
  tool_call_id: string
  tool?: string
  args?: { command?: string; path?: string; new_path?: string }
  is_error?: boolean
  note?: string
  result?: { parts: { tr?: { parts: Part[] } }[] }
}

const memoryWrites = ['create', 'str_replace', 'insert', 'rename']

// toolChips is one chip per tool call in the stream, in the order the calls
// were requested.
export function toolChips(events: UIEvent[]): ToolChip[] {
  const chips = new Map<string, ToolChip>()
  const args = new Map<string, Payload['args']>()
  for (const e of events) {
    const p = e.payload as Payload
    switch (e.type) {
      case 'tool.call.requested':
        chips.set(p.tool_call_id, { seq: e.seq, name: p.tool ?? '', state: 'running' })
        args.set(p.tool_call_id, p.args)
        break
      case 'tool.call.completed': {
        const chip = chips.get(p.tool_call_id)
        if (!chip) break
        chip.state = p.is_error ? 'failed' : 'done'
        if (p.is_error) chip.error = resultText(p)
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
        chip.state = 'interrupted'
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
