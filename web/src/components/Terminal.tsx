import { useEffect, useRef, useState } from 'react'
import { Terminal as Xterm } from 'xterm'
import { FitAddon } from 'xterm-addon-fit'
import 'xterm/css/xterm.css'
import { api } from '../api/client'

/**
 * The agent's actual terminal, in the browser.
 *
 * Not a log tail and not a progress bar: this is attached to the same tmux
 * window the CLI is running in, so what you see is what it is doing — the
 * file it is reading, the command it just ran, the question it is waiting
 * on. A headless agent prints nothing until it finishes, and eight minutes
 * of blank output is indistinguishable from a hang.
 *
 * It is read-WRITE. Typing goes to the agent's own prompt, so a person can
 * interrupt, redirect, or answer it mid-run, which is the whole reason the
 * sessions live in tmux rather than in a pipe.
 */
export function Terminal({ session }: { session: string }) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<'connecting' | 'open' | 'closed'>('connecting')

  useEffect(() => {
    if (!host.current) return
    const term = new Xterm({
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      cursorBlink: true,
      // Enough to scroll back through a long build without holding a
      // session's entire life in memory.
      scrollback: 5000,
      theme: {
        background: '#0f1113',
        foreground: '#d8dce0',
        cursor: '#d8dce0',
        selectionBackground: '#2a3038',
      },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host.current)
    fit.fit()

    // A dormant session is woken by asking for its terminal, and waking
    // takes a few seconds (a slot, a runtime pod, the window restored). So
    // the first attempt is expected to be refused, and retrying IS the
    // normal path rather than error handling.
    let closed = false
    let attempt = 0
    let lastReason = ''
    let ws: WebSocket | undefined
    const send = (m: object) => ws?.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m))
    const sendSize = () => {
      fit.fit()
      send({ c: term.cols, r: term.rows })
    }
    const connect = () => {
      if (closed) return
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      ws = new WebSocket(`${proto}//${location.host}/api/sessions/${session}/terminal`)
      ws.binaryType = 'arraybuffer'

      ws.onopen = () => {
        attempt = 0
        setState('open')
        sendSize()
      }
      ws.onmessage = (e) => {
        term.write(
          typeof e.data === 'string' ? e.data : new Uint8Array(e.data as ArrayBuffer),
        )
      }
      ws.onclose = () => {
        if (closed) return
        // Never opened means the session is waking — and waking can be
        // BLOCKED, most often because every slot in the Cell is taken. The
        // control plane already knows and has written it down, so ask,
        // rather than spinning on a generic message and then declaring a
        // "disconnection" that explains nothing.
        if (attempt === 0) {
          setState('connecting')
          term.write('\r\n\x1b[90m—— 会话在休眠,正在唤醒 ——\x1b[0m\r\n')
        }
        attempt++
        api
          .sessionState(session)
          .then((st) => {
            if (closed) return
            // Report a NEW reason as it appears, so "waiting for a slot"
            // shows up the moment it becomes the answer.
            if (st.message && st.message !== lastReason) {
              lastReason = st.message
              term.write(`\x1b[90m${st.message}\x1b[0m\r\n`)
            }
            // Keep waiting as long as the platform says it is coming. A
            // wake blocked on a slot can take as long as somebody else's
            // work does, and timing out would just hide that.
            setTimeout(connect, 3000)
          })
          .catch(() => {
            if (closed) return
            if (attempt < 12) {
              setTimeout(connect, 3000)
              return
            }
            setState('closed')
            term.write('\r\n\x1b[90m—— 连接已断开 ——\x1b[0m\r\n')
          })
      }
      ws.onerror = () => {
        /* onclose follows, and it owns the retry */
      }
    }
    connect()

    term.onData((d) => send({ d }))

    const ro = new ResizeObserver(() => sendSize())
    ro.observe(host.current)

    return () => {
      closed = true
      ro.disconnect()
      ws?.close()
      term.dispose()
    }
  }, [session])

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 6 }}>
        <span className="hint" style={{ margin: 0 }}>
          这是 agent 真正的终端——可以直接打字插话、按 Ctrl-C 打断。
        </span>
        <span className={`dot ${state === 'open' ? 'green' : state === 'closed' ? 'red' : 'amber'}`} />
      </div>
      <div
        ref={host}
        style={{
          height: 420,
          padding: 8,
          background: '#0f1113',
          border: '1px solid var(--line)',
          borderRadius: 4,
        }}
      />
    </div>
  )
}
