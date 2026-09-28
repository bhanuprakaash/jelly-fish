import { useEffect, useState } from 'react'

type HelloState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ok'; message: string }

export function useHello(): HelloState {
  const [state, setState] = useState<HelloState>({ status: 'loading' })

  useEffect(() => {
    const controller = new AbortController()

    fetch('/api/hello', { signal: controller.signal })
      .then((res) => {
        if (!res.ok) throw new Error(`status ${res.status}`)
        return res.json() as Promise<{ message: string }>
      })
      .then((body) => setState({ status: 'ok', message: body.message }))
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === 'AbortError') return
        setState({ status: 'error' })
      })

    return () => controller.abort()
  }, [])

  return state
}
