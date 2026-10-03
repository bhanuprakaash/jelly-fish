// onVisibilityChange calls hide when the page is hidden and show when it is
// visible again, and returns a function that stops listening. Streams use it
// to close on hide and reopen on show (docs/design/streaming.md D6).
export function onVisibilityChange(hide: () => void, show: () => void): () => void {
  const onChange = () => {
    if (document.visibilityState === 'hidden') hide()
    else show()
  }
  document.addEventListener('visibilitychange', onChange)
  return () => document.removeEventListener('visibilitychange', onChange)
}
