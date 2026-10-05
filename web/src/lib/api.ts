// Payload of a user.message event (internal/msg.Message, ADR 0002). Events
// arrive as stored: msg_v 1 parts carry `type`, msg_v 2 parts carry `k`. Only
// text parts have a top-level `text`.
export type Message = {
  msg_v: number
  role: string
  parts: { k?: string; type?: string; text?: string }[]
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

export type ProviderKey = { provider: string; last4: string; updated_at: string }

// getProviderKeys lists the current User's saved Provider Keys, never the keys.
export async function getProviderKeys(): Promise<ProviderKey[]> {
  const res = await request('/api/provider-keys')
  return res.json() as Promise<ProviderKey[]>
}

// putProviderKey checks, seals and saves a key; it throws HttpError 422 when
// the Provider rejects the key and 502 when the Provider can't be reached.
export async function putProviderKey(provider: string, key: string): Promise<ProviderKey> {
  const res = await request(`/api/provider-keys/${provider}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key }),
  })
  return res.json() as Promise<ProviderKey>
}

// deleteProviderKey removes the User's saved key for a Provider.
export async function deleteProviderKey(provider: string): Promise<void> {
  await request(`/api/provider-keys/${provider}`, { method: 'DELETE' })
}

// ModelPrice is a model's list price in USD per million tokens.
export type ModelPrice = { input: number; output: number }

export type ModelOption = {
  id: string
  display_name?: string
  context_window?: number
  // price is null when the catalog has no price for the model.
  price: ModelPrice | null
}

// ProviderModels is one picker group. A Provider with no saved key is not
// available: its models show but can't be picked.
export type ProviderModels = { provider: string; available: boolean; models: ModelOption[] }

export type Models = { default: string; providers: ProviderModels[] }

// getModels lists the models the User can pick, grouped per Provider.
export async function getModels(): Promise<Models> {
  const res = await request('/api/models')
  return res.json() as Promise<Models>
}

// changeModel switches a session's model for its next turn; a session
// stopped on an error re-runs that turn on it.
export async function changeModel(sessionId: string, model: string): Promise<void> {
  await request(`/api/sessions/${sessionId}/model`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model }),
  })
}

export type ChatSummary = {
  id: string
  // title is the chat's Title, else a placeholder cut from its first message.
  title: string
  titled: boolean
  status: string
  updated_at: string
  incognito: boolean
}

// getSessions lists the User's chats for the Chat List, newest activity first.
export async function getSessions(): Promise<ChatSummary[]> {
  const res = await request('/api/sessions')
  return res.json() as Promise<ChatSummary[]>
}

// renameSession sets a chat's Title; it throws HttpError 400 for a title that
// is empty or over 100 characters, and 422 for a child session.
export async function renameSession(sessionId: string, title: string): Promise<void> {
  await request(`/api/sessions/${sessionId}/title`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
}

// createSession starts a session, on model if given, else the default;
// repeating the same sessionId returns the existing one instead of sending
// message again (event-log.md §4). An incognito session never uses memory.
export function createSession(sessionId: string, clientMsgId: string, message: string, model?: string, incognito?: boolean) {
  return postJSON<{ session_id: string; last_seq: number }>('/api/sessions', {
    session_id: sessionId,
    client_msg_id: clientMsgId,
    message,
    model,
    incognito,
  })
}

// postMessage appends a follow-up message to an existing session.
export function postMessage(sessionId: string, clientMsgId: string, message: string) {
  return postJSON<{ seq: number }>(`/api/sessions/${sessionId}/messages`, {
    client_msg_id: clientMsgId,
    message,
  })
}

// interruptSession is "Stop retrying": it parks a sleeping session, and stops
// a turn a Worker has just started.
export async function interruptSession(sessionId: string): Promise<void> {
  await request(`/api/sessions/${sessionId}/interrupt`, { method: 'POST' })
}

// retrySession is "Retry now" after a Provider error.
export async function retrySession(sessionId: string): Promise<void> {
  await request(`/api/sessions/${sessionId}/retry`, { method: 'POST' })
}

export type Memory = {
  id: string
  scope: 'user' | 'project'
  path: string
  title: string
  kind: string
  content: string
  status: 'active' | 'pending_review'
  version: number
  // stale is true when the memory was last read over 90 days ago.
  stale: boolean
  // source_session_id is null once the session that wrote it is deleted.
  source_session_id: string | null
  updated_at: string
}

export type MemoryProject = { id: string; name: string; use_user_memory: boolean }

export type MemoryRevision = {
  version: number
  path: string
  title: string
  content: string
  written_by: 'agent' | 'user' | 'tidy'
  created_at: string
}

// getMemories lists User Memory and the Personal project's Memory, pending
// entries included.
export async function getMemories(): Promise<{ project: MemoryProject; memories: Memory[] }> {
  const res = await request('/api/memories')
  return res.json() as Promise<{ project: MemoryProject; memories: Memory[] }>
}

// getMemoryRevisions lists every version of a memory, oldest first.
export async function getMemoryRevisions(id: string): Promise<MemoryRevision[]> {
  const res = await request(`/api/memories/${id}/revisions`)
  return res.json() as Promise<MemoryRevision[]>
}

// editMemory saves new content. It throws HttpError 422 when the text is
// empty, over 4 KB or looks like a secret. Editing a pending memory approves it.
export async function editMemory(id: string, content: string): Promise<void> {
  await request(`/api/memories/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  })
}

// approveMemory makes a pending memory active.
export async function approveMemory(id: string): Promise<void> {
  await request(`/api/memories/${id}/approve`, { method: 'POST' })
}

// deleteMemory hard-deletes a memory and its history.
export async function deleteMemory(id: string): Promise<void> {
  await request(`/api/memories/${id}`, { method: 'DELETE' })
}

// undoMemory reverses the chat write that left the memory at version; it
// throws HttpError 409 when the memory has changed since.
export async function undoMemory(id: string, version: number): Promise<void> {
  await request(`/api/memories/${id}/undo`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ version }),
  })
}

// setUseUserMemory sets whether new chats of the project use User Memory.
export async function setUseUserMemory(projectId: string, use: boolean): Promise<void> {
  await request(`/api/projects/${projectId}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ use_user_memory: use }),
  })
}

// Delta is an ephemeral fragment of a streaming reply (streaming.md §4.1).
export type Delta = {
  turn_id: string
  idx: number
  kind: string
  text: string
}
