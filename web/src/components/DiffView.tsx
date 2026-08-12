import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'

/**
 * Unified-diff rendering for one settled session. Patches come from the
 * forge via the broker, so celld never holds a credential to fetch them.
 */
export function DiffView({ session }: { session: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['diff', session],
    queryFn: () => api.diff(session),
    refetchInterval: false,
    staleTime: 60_000,
  })

  if (isLoading) return <div className="diff">加载 diff…</div>
  if (error) return <div className="diff err">取 diff 失败:{(error as Error).message}</div>

  const files = data?.files ?? []
  if (files.length === 0) return <div className="diff">(无文件变更)</div>

  return (
    <div className="diff">
      <div className="hunk">
        {files.length} 个文件 · <span className="add">+{data?.additions ?? 0}</span>{' '}
        <span className="del">-{data?.deletions ?? 0}</span>
      </div>
      {files.map((f) => (
        <div key={f.filename}>
          <div className="file">
            {f.status} {f.filename}{' '}
            <span className="add">+{f.additions}</span>{' '}
            <span className="del">-{f.deletions}</span>
          </div>
          {f.patch && (
            <pre>
              {f.patch.split('\n').map((line, i) => (
                <div
                  key={i}
                  className={
                    line.startsWith('+')
                      ? 'add'
                      : line.startsWith('-')
                        ? 'del'
                        : line.startsWith('@@')
                          ? 'hunk'
                          : undefined
                  }
                >
                  {line}
                </div>
              ))}
            </pre>
          )}
        </div>
      ))}
    </div>
  )
}
