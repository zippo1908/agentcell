import { useEffect, useRef, useState } from 'react'
import type { Cell } from '../api/types'

/**
 * The resident dev-server preview. It deliberately does NOT reload on every
 * poll — that would fight the user's scroll position; reloading is manual,
 * and switching follow-target changes the key so the frame remounts.
 */
export function PreviewPane({
  cell,
  onRelease,
  releasing,
}: {
  cell: Cell
  onRelease: () => void
  releasing: boolean
}) {
  const [zone, setZone] = useState<'preview' | 'prod'>('preview')
  const frame = useRef<HTMLIFrameElement>(null)
  // The server hands us absolute, ticketed URLs on the untrusted-content
  // origin (ADR-0007). Composing them here from paths would put the content
  // back on the console's origin and collapse the isolation.
  const src = zone === 'prod' ? cell.productionURL : cell.previewURL

  // Follow-target changes mean a different working tree is being served.
  useEffect(() => {
    if (frame.current && src) frame.current.src = src
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cell.followSession, zone])

  return (
    <div className="card preview">
      <div className="bar">
        <h2 style={{ margin: 0, flex: 1 }}>
          {zone === 'prod' ? '正式区' : '开发区预览(常驻)'}
        </h2>
        <span className="status">
          {zone === 'preview'
            ? cell.followSession
              ? `跟随会话 ${cell.followSession.slice(0, 8)}…`
              : '显示主检出'
            : cell.releaseRef || '当前发布'}
        </span>
        <button
          className="ghost"
          onClick={() => setZone(zone === 'prod' ? 'preview' : 'prod')}
          disabled={zone === 'preview' && !cell.productionPath}
          title={
            !cell.productionPath ? '还没有发布过,正式区不存在' : '在开发区/正式区之间切换'
          }
        >
          {zone === 'prod' ? '看开发区' : '看正式区'}
        </button>
        <button
          className="ghost"
          onClick={() => {
            if (frame.current) frame.current.src = src
          }}
        >
          刷新
        </button>
        <button onClick={onRelease} disabled={releasing} title="开发调试不影响正式区">
          {releasing ? '发布中…' : '发布到正式区'}
        </button>
      </div>
      {src ? (
        <iframe
          ref={frame}
          src={src}
          title="product preview"
          // The previewed app is untrusted, but it is served from its OWN
          // origin (ADR-0007), so allow-same-origin is safe to grant and the
          // app behaves exactly as it would standalone — cookies, storage
          // and service workers all work. What stays denied is replacing or
          // navigating this console page. celld sends the same policy as a
          // CSP header so it also holds when the URL is opened directly.
          sandbox="allow-same-origin allow-scripts allow-forms allow-modals allow-popups allow-downloads"
        />
      ) : (
        <div className="empty">正式区尚未发布。</div>
      )}
    </div>
  )
}
