import { useHello } from './useHello'

function App() {
  const hello = useHello()

  return (
    <main className="flex min-h-svh items-center justify-center bg-neutral-950 px-4 text-neutral-100">
      <div className="w-full max-w-sm rounded-2xl bg-neutral-900 p-8 text-center shadow-xl sm:max-w-md">
        <h1 className="text-2xl font-semibold sm:text-3xl">Jelly-fish</h1>
        <p className="mt-4 text-lg text-neutral-300">
          {hello.status === 'loading' && 'Loading…'}
          {hello.status === 'error' && 'Could not reach the API.'}
          {hello.status === 'ok' && hello.message}
        </p>
      </div>
    </main>
  )
}

export default App
