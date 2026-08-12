import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { Badge, SkeletonTable, Tag } from '../ui/primitives'
import { NONE } from '../lib/format'

/**
 * What this deployment can actually run.
 *
 * Worth its own page because the answer is configuration, not code: runners
 * and providers are preset tables an operator overrides, so "why can't I
 * pick X" should be answerable by looking rather than by reading source.
 */
export function CapabilitiesPage() {
  const { data: meta, isLoading } = useQuery({ queryKey: ['meta'], queryFn: api.meta, staleTime: 60_000 })
  const runners = meta?.runners ?? []
  const providers = meta?.providers ?? []
  const byName = Object.fromEntries(providers.map((p) => [p.name, p]))

  return (
    <>
      <h1 className="page-title">
        能力
        <span className="sub">这套部署能跑什么</span>
      </h1>

      <div className="card">
        <h3>Agent CLI</h3>
        {isLoading ? (
          <SkeletonTable rows={3} cols={4} />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>CLI</th>
                  <th>厂商</th>
                  <th>协议</th>
                  <th>续话</th>
                  <th>可搭配</th>
                </tr>
              </thead>
              <tbody>
                {runners.map((r) => (
                  <tr key={r.name}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{r.display}</div>
                      <div className="mono faint">{r.name}</div>
                    </td>
                    <td className="muted">{r.vendor || NONE}</td>
                    <td>
                      <div className="btn-row">
                        {r.protocols.map((p) => (
                          <Tag key={p}>{p}</Tag>
                        ))}
                      </div>
                    </td>
                    <td>
                      {r.resumable ? (
                        <Badge tone="green">支持</Badge>
                      ) : (
                        <Badge tone="gray">未声明</Badge>
                      )}
                    </td>
                    <td className="muted">
                      {r.providers.map((p) => byName[p]?.display ?? p).join('、') || NONE}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="hint" style={{ marginTop: 12 }}>
          「未声明」表示这个 CLI 的会话续接方式我们没有验证过 —— 常驻会话里追加的指令会新开一个对话,
          而不是接着上一个。确认过你固定的版本之后,往 <code className="mono">runners.d/</code> 丢一个 YAML
          就能补上,不需要发版。
        </div>
      </div>

      <div className="card">
        <h3>模型来源</h3>
        {isLoading ? (
          <SkeletonTable rows={4} cols={4} />
        ) : (
          <div className="table-wrap">
            <table className="data">
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>区域</th>
                  <th>协议</th>
                  <th>模型</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {providers.map((p) => (
                  <tr key={p.name}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{p.display}</div>
                      <div className="mono faint">{p.name}</div>
                    </td>
                    <td>
                      {p.region === 'cn' ? <Badge tone="green">境内</Badge> : <Badge tone="gray">境外</Badge>}
                    </td>
                    <td>
                      <div className="btn-row">
                        {p.protocols.map((x) => (
                          <Tag key={x}>{x}</Tag>
                        ))}
                      </div>
                    </td>
                    <td className="muted" style={{ maxWidth: 320 }}>
                      {(p.models ?? []).join('、') || <span className="faint">按 provider 默认</span>}
                    </td>
                    <td>{p.docs && <a href={p.docs} target="_blank" rel="noreferrer">文档</a>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="hint" style={{ marginTop: 12 }}>
          模型清单只是起点:厂商上新的速度远快于这张表,派工时可以直接填一个不在清单里的。
        </div>
      </div>
    </>
  )
}
