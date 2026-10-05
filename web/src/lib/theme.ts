// themes lists every theme the user can pick; index.html repeats it to set the first paint.
export const themes = ['system', 'jellyfish-light', 'jellyfish-dark', 'classic'] as const
// Theme is one of the pickable themes.
export type Theme = (typeof themes)[number]

const dark = window.matchMedia('(prefers-color-scheme: dark)')

// getTheme returns the saved theme, or "system" when none is saved or storage is blocked.
export function getTheme(): Theme {
  let stored: string | null = null
  try {
    stored = localStorage.getItem('theme')
  } catch {
    // Storage can throw SecurityError when blocked.
  }
  return themes.find((t) => t === stored) ?? 'system'
}

function apply(theme: Theme) {
  const root = document.documentElement
  root.dataset.theme = theme === 'system' ? (dark.matches ? 'jellyfish-dark' : 'jellyfish-light') : theme
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute('content', getComputedStyle(root).getPropertyValue('--bg').trim())
}

// initTheme applies the saved theme and follows OS changes while it is "system".
export function initTheme() {
  apply(getTheme())
  dark.addEventListener('change', () => apply(getTheme()))
}

// setTheme applies the theme and saves it; blocked storage still leaves it applied for this page.
export function setTheme(theme: Theme) {
  try {
    localStorage.setItem('theme', theme)
  } catch {
    // Storage can throw SecurityError when blocked.
  }
  apply(theme)
}
