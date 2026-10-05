export const themes = ['system', 'jellyfish-light', 'jellyfish-dark', 'classic'] as const
export type Theme = (typeof themes)[number]

const dark = window.matchMedia('(prefers-color-scheme: dark)')

export function getTheme(): Theme {
  const stored = localStorage.getItem('theme')
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

export function setTheme(theme: Theme) {
  localStorage.setItem('theme', theme)
  apply(theme)
}
