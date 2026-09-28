// Payload of a user.message event (internal/msg.Message, ADR 0002).
export type Message = {
  msg_v: number
  role: string
  parts: { type: string; text?: string }[]
}

export type UIEvent = {
  seq: number
  type: string
  created_at: string
  payload: unknown
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(`${path}: status ${res.status}`)
  }
  return res.json() as Promise<T>
}

// createSession starts a session; repeating the same sessionId returns the
// existing one instead of sending message again (event-log.md §4).
export function createSession(sessionId: string, clientMsgId: string, message: string) {
  return postJSON<{ session_id: string; last_seq: number }>('/api/sessions', {
    session_id: sessionId,
    client_msg_id: clientMsgId,
    message,
  })
}

// postMessage appends a follow-up message to an existing session.
export function postMessage(sessionId: string, clientMsgId: string, message: string) {
  return postJSON<{ seq: number }>(`/api/sessions/${sessionId}/messages`, {
    client_msg_id: clientMsgId,
    message,
  })
}
