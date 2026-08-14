import { useCallback, useEffect, useRef, useState } from 'react'

/**
 * A draggable divider between two columns.
 *
 * Widths are remembered per pane, because a layout you have to re-adjust
 * every visit is worse than one that never moved: people size the terminal
 * once for how they work — a wide project list on a big screen, almost none
 * on a laptop — and that is a preference, not a session detail.
 */
export function usePaneWidth(key: string, initial: number, min: number, max: number) {
  const [w, setW] = useState<number>(() => {
    const saved = Number(localStorage.getItem('ws-w-' + key))
    return saved >= min && saved <= max ? saved : initial
  })
  const set = useCallback(
    (next: number) => {
      const clamped = Math.min(max, Math.max(min, next))
      setW(clamped)
      localStorage.setItem('ws-w-' + key, String(clamped))
    },
    [key, min, max],
  )
  return [w, set] as const
}

export function Splitter({
  onDrag,
  side = 'left',
  title,
}: {
  /** Called with the pointer's x while dragging. */
  onDrag: (clientX: number) => void
  side?: 'left' | 'right'
  title?: string
}) {
  const dragging = useRef(false)

  useEffect(() => {
    const move = (e: PointerEvent) => {
      if (!dragging.current) return
      // While dragging, the pointer is the only thing that matters: stop the
      // browser turning it into a text selection across the whole page.
      e.preventDefault()
      onDrag(e.clientX)
    }
    const up = () => {
      dragging.current = false
      document.body.classList.remove('resizing')
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
  }, [onDrag])

  return (
    <div
      className="splitter"
      role="separator"
      aria-orientation="vertical"
      title={title ?? '拖动调整宽度'}
      data-side={side}
      onPointerDown={(e) => {
        e.preventDefault()
        dragging.current = true
        document.body.classList.add('resizing')
      }}
      onDoubleClick={() => onDrag(-1)}
    />
  )
}
