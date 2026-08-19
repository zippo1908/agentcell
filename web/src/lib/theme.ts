export type Theme = 'light' | 'dark' | 'system'

const KEY = 'agentcell-theme'
const DARK = '(prefers-color-scheme: dark)'

export function getTheme(): Theme {
  const t = localStorage.getItem(KEY)
  // Anything unrecognised — including nothing stored yet — means "follow
  // the OS", so a stale or hand-edited value degrades to the safe default.
  return t === 'light' || t === 'dark' ? t : 'system'
}

export function setTheme(t: Theme) {
  localStorage.setItem(KEY, t)
  applyTheme(t)
}

function resolve(t: Theme): 'light' | 'dark' {
  if (t !== 'system') return t
  return window.matchMedia(DARK).matches ? 'dark' : 'light'
}

/* The theme is a data attribute, not a class, because it is state about
   the document — and the CSS can key off [data-theme] before any React
   tree exists to put a class somewhere. */
export function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = resolve(t)
}

/* Runs before the first render (see main.tsx): applying later would paint
   one frame of the wrong theme, which on a dark room's screen is a flash
   of white the user absolutely notices. */
export function initTheme() {
  applyTheme(getTheme())
  // Track the OS only while the user asked us to. Once they pick a side,
  // their choice wins over the clock — re-resolving on every system change
  // would silently override an explicit preference.
  window.matchMedia(DARK).addEventListener('change', () => {
    if (getTheme() === 'system') applyTheme('system')
  })
}
