const key = 'show-thinking'

// getShowThinking returns the saved "Show thinking" choice; off when none is saved or storage is blocked.
export function getShowThinking(): boolean {
  try {
    return localStorage.getItem(key) === 'true'
  } catch {
    // Storage can throw SecurityError when blocked.
    return false
  }
}

// setShowThinking saves the choice; blocked storage still leaves it applied for this page.
export function setShowThinking(show: boolean) {
  try {
    localStorage.setItem(key, String(show))
  } catch {
    // Storage can throw SecurityError when blocked.
  }
}
