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

// UnauthorizedError means the server answered 401: there is no valid Login
// Session, so the app shows the login screen.
export class UnauthorizedError extends Error {
  constructor(path: string) {
    super(`${path}: status 401`)
  }
}

// HttpError is any other non-2xx answer, with its status for callers that
// word it differently.
export class HttpError extends Error {
  status: number

  constructor(path: string, status: number) {
    super(`${path}: status ${status}`)
    this.status = status
  }
}

async function request(path: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(path, init)
  if (res.status === 401) {
    throw new UnauthorizedError(path)
  }
  if (!res.ok) {
    throw new HttpError(path, res.status)
  }
  return res
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await request(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return res.json() as Promise<T>
}

export type Me = { id: string; email: string; name: string; is_admin: boolean }

// getMe returns the signed-in User, or throws UnauthorizedError.
export async function getMe(): Promise<Me> {
  const res = await request('/api/me')
  return res.json() as Promise<Me>
}

// requestCode emails a sign-in code. It succeeds the same for any email, so
// it cannot be used to tell who has an account.
export async function requestCode(email: string): Promise<void> {
  await request('/api/auth/code', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  })
}

// verifyCode signs in with the emailed code and resolves true, or false when
// the code is wrong or expired.
export async function verifyCode(email: string, code: string): Promise<boolean> {
  try {
    await request('/api/auth/code/verify', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, code }),
    })
    return true
  } catch (err) {
    if (err instanceof UnauthorizedError) return false
    throw err
  }
}

// verifyLink signs in with the token from the emailed link and resolves true,
// or false when the link is used or expired.
export async function verifyLink(token: string): Promise<boolean> {
  try {
    await request('/api/auth/link', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ t: token }),
    })
    return true
  } catch (err) {
    if (err instanceof UnauthorizedError) return false
    throw err
  }
}

export type LoginSession = {
  id: string
  user_agent: string
  created_at: string
  last_seen_at: string
  current: boolean
}

// getLoginSessions lists the signed-in devices of the current User.
export async function getLoginSessions(): Promise<LoginSession[]> {
  const res = await request('/api/me/login-sessions')
  return res.json() as Promise<LoginSession[]>
}

// deleteLoginSession logs one device out, possibly this one.
export async function deleteLoginSession(id: string): Promise<void> {
  await request(`/api/me/login-sessions/${id}`, { method: 'DELETE' })
}

// logout ends this device's Login Session.
export async function logout(): Promise<void> {
  await request('/api/auth/logout', { method: 'POST' })
}

// logoutAll ends every Login Session of the current User, this one included.
export async function logoutAll(): Promise<void> {
  await request('/api/auth/logout-all', { method: 'POST' })
}

export type AdminUser = {
  id: string
  email: string
  name: string
  is_admin: boolean
  disabled_at: string | null
  created_at: string
}

export type AdminInvite = {
  id: string
  email: string
  expires_at: string
  created_at: string
}

// getAdminUsers lists every User and open Invite. Non-admins get a 404.
export async function getAdminUsers(): Promise<{ users: AdminUser[]; invites: AdminInvite[] }> {
  const res = await request('/api/admin/users')
  return res.json() as Promise<{ users: AdminUser[]; invites: AdminInvite[] }>
}

// createInvite emails an Invite to email; inviting an open Invite's email again
// re-sends it.
export async function createInvite(email: string): Promise<void> {
  await postJSON('/api/admin/invites', { email })
}

// resendInvite emails an Invite again and resets its expiry.
export async function resendInvite(id: string): Promise<void> {
  await request(`/api/admin/invites/${id}/resend`, { method: 'POST' })
}

// revokeInvite deletes an open Invite.
export async function revokeInvite(id: string): Promise<void> {
  await request(`/api/admin/invites/${id}`, { method: 'DELETE' })
}

// setUserDisabled disables or enables a User; disabling ends their Login Sessions.
export async function setUserDisabled(id: string, disabled: boolean): Promise<void> {
  await request(`/api/admin/users/${id}/${disabled ? 'disable' : 'enable'}`, { method: 'POST' })
}

// makeAdmin promotes a User to Admin.
export async function makeAdmin(id: string): Promise<void> {
  await request(`/api/admin/users/${id}/make-admin`, { method: 'POST' })
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

// Delta is an ephemeral fragment of a streaming reply (streaming.md §4.1).
export type Delta = {
  turn_id: string
  idx: number
  kind: string
  text: string
}
