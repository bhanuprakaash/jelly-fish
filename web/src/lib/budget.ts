import type { UIEvent } from './api'

export type Budget = { tokens: number; cost_micros: number; turns: number }

// sessionBudget is the limits the session's next check uses: the latest
// session.config_changed budget, else the one session.created set.
export function sessionBudget(events: UIEvent[]): Budget | undefined {
  let budget: Budget | undefined
  for (const e of events) {
    if (e.type === 'session.created') {
      budget = (e.payload as { agent?: { budget?: Budget } }).agent?.budget
    } else if (e.type === 'session.config_changed') {
      budget = (e.payload as { budget?: Budget }).budget ?? budget
    }
  }
  return budget
}

export function budgetHint(b?: Budget): string {
  if (!b) return 'Budget · none set · /budget <dollars> <tokens> <turns>'
  const tokens = b.tokens >= 1000 ? `${Math.round(b.tokens / 1000)}k` : `${b.tokens}`
  return `Budget · $${(b.cost_micros / 1e6).toFixed(2)} · ${tokens} tokens · ${b.turns} turns per message`
}

// parseBudget reads "<dollars> [tokens] [turns]"; a limit left out keeps its
// current value, so all three are required when there is none. It returns
// undefined for anything that isn't a positive number.
export function parseBudget(args: string, current?: Budget): Budget | undefined {
  const [dollars, tokens, turns, ...extra] = args.trim().split(/\s+/)
  const next: Budget = {
    cost_micros: Math.round(Number(dollars) * 1e6),
    tokens: tokens === undefined ? (current?.tokens ?? NaN) : Number(tokens),
    turns: turns === undefined ? (current?.turns ?? NaN) : Number(turns),
  }
  const valid = extra.length === 0 && Object.values(next).every((n) => Number.isInteger(n) && n > 0)
  return valid ? next : undefined
}
