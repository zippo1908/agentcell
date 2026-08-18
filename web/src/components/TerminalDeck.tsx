import { useEffect, useState } from 'react'
import { Terminal } from './Terminal'

/**
 * Keeps the terminals you have visited ALIVE while you look at another one.
 *
 * Switching projects used to unmount the terminal, which closed its
 * websocket, dropped whatever was scrolling past and made you pay for a
 * fresh attach on the way back — so moving between two projects to compare
 * them cost a reconnect each way, and any output that arrived while you
 * were gone was simply gone. Hiding rather than unmounting keeps each one
 * attached and receiving; coming back is instant and complete.
 *
 * Not unbounded: every live terminal is a websocket and a scrollback
 * buffer, so only the most recently viewed few stay mounted and the rest
 * are dropped, oldest first. That is a real disconnect for those — bounded
 * memory is worth more than a session you have not looked at in an hour.
 */
const MAX_LIVE = 6

export function TerminalDeck({ session }: { session: string }) {
  // Most recently viewed first; the tail is what gets evicted.
  const [live, setLive] = useState<string[]>(() => (session ? [session] : []))

  useEffect(() => {
    if (!session) return
    setLive((prev) =>
      prev[0] === session ? prev : [session, ...prev.filter((s) => s !== session)].slice(0, MAX_LIVE),
    )
  }, [session])

  return (
    <>
      {live.map((s) => (
        // hidden, not unmounted: unmounting is what closed the socket. The
        // hidden ones keep reading and keep their scrollback. The attribute
        // rather than an inline style, so the pane's own layout rules still
        // decide how the visible one is sized.
        <div key={s} className="term-slot" hidden={s !== session}>
          <Terminal session={s} />
        </div>
      ))}
    </>
  )
}
