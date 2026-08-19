import type { ReactNode } from 'react'

/**
 * Markdown for the agent's answers, rendered as React elements — never as
 * HTML strings, so there is no injection surface to sanitize. The agent
 * writes GitHub-flavoured markdown and the board used to show it raw, pipes
 * and asterisks and all; this is the subset those answers actually use:
 * fenced code, tables, headers, lists, quotes, rules, links, bold/italic,
 * inline code. Anything outside the subset degrades to plain text, never to
 * a broken render.
 */
export function Markdown({ text }: { text: string }) {
  return <div className="md">{renderBlocks(text)}</div>
}

function renderBlocks(text: string): ReactNode[] {
  const lines = text.split('\n')
  const out: ReactNode[] = []
  let i = 0
  let key = 0
  while (i < lines.length) {
    const line = lines[i]

    // Fenced code: everything to the closing fence is verbatim.
    if (/^```/.test(line)) {
      const buf: string[] = []
      i++
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        buf.push(lines[i])
        i++
      }
      i++ // the closing fence itself
      out.push(
        <pre key={key++} className="md-code">
          {buf.join('\n')}
        </pre>,
      )
      continue
    }

    // Tables: a header row of pipes, a |---| rule, then body rows.
    if (/^\|.*\|\s*$/.test(line) && i + 1 < lines.length && /^\|[\s:|-]+\|\s*$/.test(lines[i + 1])) {
      const cells = (l: string) => l.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim())
      const head = cells(line)
      const rows: string[][] = []
      i += 2
      while (i < lines.length && /^\|.*\|\s*$/.test(lines[i])) {
        rows.push(cells(lines[i]))
        i++
      }
      out.push(
        <table key={key++} className="md-table">
          <thead>
            <tr>
              {head.map((c, j) => (
                <th key={j}>{inline(c)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, k) => (
              <tr key={k}>
                {r.map((c, j) => (
                  <td key={j}>{inline(c)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>,
      )
      continue
    }

    // Headers carry their level; a rule is three or more dashes alone.
    const h = /^(#{1,4})\s+(.*)$/.exec(line)
    if (h) {
      const level = h[1].length
      out.push(
        <div key={key++} className={`md-h md-h${level}`}>
          {inline(h[2])}
        </div>,
      )
      i++
      continue
    }
    if (/^\s*(-{3,}|\*{3,})\s*$/.test(line)) {
      out.push(<hr key={key++} className="md-hr" />)
      i++
      continue
    }

    // Lists: consecutive markers share one block. Ordered and unordered
    // render alike — the number is content, not structure.
    if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*([-*+]|\d+\.)\s+/.test(lines[i])) {
        items.push(lines[i].replace(/^\s*([-*+]|\d+\.)\s+/, ''))
        i++
      }
      out.push(
        <ul key={key++} className="md-list">
          {items.map((it, k) => (
            <li key={k}>{inline(it)}</li>
          ))}
        </ul>,
      )
      continue
    }

    if (/^>\s?/.test(line)) {
      const buf: string[] = []
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        buf.push(lines[i].replace(/^>\s?/, ''))
        i++
      }
      out.push(
        <blockquote key={key++} className="md-quote">
          {inline(buf.join(' '))}
        </blockquote>,
      )
      continue
    }

    // A blank line ends a paragraph; consecutive text lines join with a
    // space, because that is how prose wraps.
    if (line.trim() === '') {
      i++
      continue
    }
    const buf: string[] = [line]
    i++
    while (i < lines.length && lines[i].trim() !== '' && !/^(```|#{1,4}\s|\||>|-{3,}|\s*([-*+]|\d+\.)\s)/.test(lines[i])) {
      buf.push(lines[i])
      i++
    }
    out.push(<p key={key++}>{inline(buf.join(' '))}</p>)
  }
  return out
}

/**
 * Inline marks, parsed left to right in one pass. Order decides precedence:
 * code spans protect their contents from every other rule, which is the one
 * property a hand parser owes its readers.
 */
function inline(text: string): ReactNode[] {
  const out: ReactNode[] = []
  let key = 0
  let rest = text
  const push = (s: string) => {
    if (s) out.push(s)
  }
  while (rest.length) {
    let m = /^(.*?)`([^`]+)`/.exec(rest)
    if (m) {
      push(m[1])
      out.push(
        <code key={key++} className="mono">
          {m[2]}
        </code>,
      )
      rest = rest.slice(m[0].length)
      continue
    }
    m = /^(.*?)\*\*([^*]+)\*\*/.exec(rest)
    if (m) {
      push(m[1])
      out.push(<strong key={key++}>{m[2]}</strong>)
      rest = rest.slice(m[0].length)
      continue
    }
    m = /^(.*?)\*([^*]+)\*/.exec(rest)
    if (m) {
      push(m[1])
      out.push(<em key={key++}>{m[2]}</em>)
      rest = rest.slice(m[0].length)
      continue
    }
    m = /^(.*?)\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/.exec(rest)
    if (m) {
      push(m[1])
      out.push(
        <a key={key++} href={m[3]} target="_blank" rel="noreferrer">
          {m[2]}
        </a>,
      )
      rest = rest.slice(m[0].length)
      continue
    }
    out.push(rest)
    break
  }
  return out
}
