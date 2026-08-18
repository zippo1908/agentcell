import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { useToast } from '../ui/primitives'

/**
 * Onboarding a project used to require cellctl, which made the console a
 * viewer of projects it could never start.
 *
 * It then asked six free-text questions, one of which was a container image
 * path. Every one of those is something the platform already knows — which
 * devboxes an operator offers, which git credentials exist, which runners
 * and providers are configured, which machine pools were defined. Asking a
 * person to retype an answer the system has is how a project ends up in
 * ImagePullBackOff over a typo made two layers away.
 *
 * So it offers. Only the repository URL and a name are typed, because only
 * those are genuinely new information.
 */
export function CellNewPage() {
  const nav = useNavigate()
  const toast = useToast()
  const [f, setF] = useState({
    name: '',
    repoURL: '',
    branch: 'main',
    secretName: '',
    image: '',
    runner: '',
    provider: '',
    model: '',
    placementClass: '',
    description: '',
    preview: '',
    previewPort: 3000,
    maxSessions: 2,
    productionTarget: 'incell',
    externalURL: '',
    webhookURL: '',
    webhookSecret: '',
  })
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) =>
    setF({ ...f, [k]: e.target.value })
  const pick = (k: keyof typeof f, v: string | number) => setF({ ...f, [k]: v })

  const opts = useQuery({ queryKey: ['new-project-options'], queryFn: () => api.newProjectOptions() })

  // Preselect the obvious: the first devbox, the only git credential, the
  // runner's own vendor. A form that starts empty makes somebody choose
  // things they have no opinion about.
  useEffect(() => {
    const o = opts.data
    if (!o) return
    setF((cur) => ({
      ...cur,
      image: cur.image || o.devboxes[0]?.image || '',
      secretName: cur.secretName || (o.gitCredentials.length === 1 ? o.gitCredentials[0] : cur.secretName),
      // The deployment's default first; the first entry only as a fallback,
      // so a team that has decided does not re-decide on every project.
      runner: cur.runner || o.defaultRunner || o.runners[0]?.name || '',
      provider:
        cur.provider || o.defaultProvider || o.runners[0]?.vendor || o.providers[0]?.name || '',
    }))
  }, [opts.data])

  const create = useMutation({
    mutationFn: () =>
      api.createCell({
        ...f,
        previewPort: Number(f.previewPort) || 3000,
        maxSessions: Number(f.maxSessions) || 2,
      }),
    onSuccess: (r) => {
      toast.success(`工作区 ${r.cell} 已创建`)
      nav(`/cells/${r.cell}`)
    },
    onError: (e) => toast.error((e as Error).message),
  })

  const ok = f.name.trim() && f.repoURL.trim() && f.image.trim()

  return (
    <>
      <h1 className="page-title">新建工作区</h1>
      <div className="card" style={{ maxWidth: 720 }}>
        <div className="form-section-title">项目</div>
        <label className="field">
          <span>名称</span>
          <input value={f.name} onChange={set('name')} placeholder="shop" />
          <em>小写字母、数字和短横;它会成为命名空间和预览域名的一部分。</em>
        </label>
        <label className="field">
          <span>仓库地址</span>
          <input value={f.repoURL} onChange={set('repoURL')} placeholder="https://git.tinci.com/team/shop.git" />
          <em>agent 在这个仓库上干活,产出推成 session/&lt;id&gt; 分支等你批阅。</em>
        </label>
        <div className="row">
          <label className="field" style={{ flex: 1 }}>
            <span>基线分支</span>
            <input value={f.branch} onChange={set('branch')} placeholder="main" />
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span>git 凭据</span>
            <select value={f.secretName} onChange={set('secretName')}>
              <option value="">不需要(公开仓库)</option>
              {(opts.data?.gitCredentials ?? []).map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
        </div>

        <div className="form-section-title">环境</div>
        <div className="pick-grid">
          {(opts.data?.devboxes ?? []).map((d) => (
            <button
              type="button"
              key={d.name}
              className={`pick ${f.image === d.image ? 'on' : ''}`}
              onClick={() => pick('image', d.image)}
            >
              <span className="pick-name">{d.displayName}</span>
              <span className="pick-sub">{d.size}</span>
              <span className="pick-desc">{d.description}</span>
            </button>
          ))}
        </div>

        <div className="form-section-title">agent 与模型</div>
        <div className="pick-grid">
          {(opts.data?.runners ?? []).map((r) => (
            <button
              type="button"
              key={r.name}
              className={`pick ${f.runner === r.name ? 'on' : ''}`}
              onClick={() => setF({ ...f, runner: r.name, provider: r.vendor || f.provider })}
            >
              <span className="pick-name">{r.display || r.name}</span>
              <span className="pick-sub">{r.vendor}</span>
            </button>
          ))}
        </div>
        <div className="pick-grid">
          {(opts.data?.providers ?? [])
            .filter((p) => {
              // Only providers this runner can actually drive. The server
              // computes that list; recomputing it here from protocols would
              // be a second implementation of the same rule, free to drift.
              const r = opts.data?.runners.find((x) => x.name === f.runner)
              return !r || r.providers.includes(p.name)
            })
            .map((p) => (
              <button
                type="button"
                key={p.name}
                className={`pick ${f.provider === p.name ? 'on' : ''}`}
                onClick={() => pick('provider', p.name)}
              >
                <span className="pick-name">{p.display || p.name}</span>
                {p.region && <span className="pick-sub">{p.region}</span>}
              </button>
            ))}
        </div>

        {/* Machine pools appear only when there is more than one. A choice
            of one implies there is something to decide. */}
        {(opts.data?.placementClasses?.length ?? 0) > 1 && (
          <>
            <div className="form-section-title">跑在哪类机器上</div>
            <div className="pick-grid">
              {opts.data!.placementClasses.map((c) => (
                <button
                  type="button"
                  key={c.name}
                  className={`pick ${f.placementClass === c.name ? 'on' : ''}`}
                  onClick={() => pick('placementClass', c.name)}
                >
                  <span className="pick-name">{c.displayName || c.name}</span>
                  <span className="pick-sub">{c.nodes} 台{c.tolerated ? ' · 专用' : ''}</span>
                  <span className="pick-desc">{c.description}</span>
                </button>
              ))}
            </div>
          </>
        )}
        <label className="field">
          <span className="lbl">预览命令</span>
          <input value={f.preview} onChange={set('preview')} placeholder="npm run dev -- --host" />
          <div className="hint">留空则不起预览。它跑的是仓库里的代码,所以只在预览专用的 origin 上提供服务。</div>
        </label>

        <div className="form-section-title">正式区</div>
        <div className="seg-row">
          <label className={`seg ${f.productionTarget === 'incell' ? 'on' : ''}`}>
            <input
              type="radio"
              style={{ display: 'none' }}
              checked={f.productionTarget === 'incell'}
              onChange={() => setF({ ...f, productionTarget: 'incell' })}
            />
            <div className="seg-title">在 Cell 内部署</div>
            <div className="seg-desc">
              平台起一个隔离的正式区跑同一条命令。开发区怎么调都不影响它,发布是唯一入口。
            </div>
          </label>
          <label className={`seg ${f.productionTarget === 'external' ? 'on' : ''}`}>
            <input
              type="radio"
              style={{ display: 'none' }}
              checked={f.productionTarget === 'external'}
              onChange={() => setF({ ...f, productionTarget: 'external' })}
            />
            <div className="seg-title">交给外部部署</div>
            <div className="seg-desc">
              平台只负责宣布「发布了什么」,由你现有的流水线去跑。生产系统是别人的,就不该由这里代管。
            </div>
          </label>
        </div>
        {f.productionTarget === 'external' && (
          <div style={{ marginTop: 14 }}>
            <label className="field">
              <span className="lbl">正式环境地址</span>
              <input value={f.externalURL} onChange={set('externalURL')} placeholder="https://shop.example.com" />
              <div className="hint">控制台只做跳转,不代理 —— 它不是我们的 origin,也有自己的鉴权。</div>
            </label>
            <div className="row">
              <label className="field" style={{ flex: 1 }}>
                <span className="lbl">发布 Webhook</span>
                <input value={f.webhookURL} onChange={set('webhookURL')} placeholder="https://ci.example.com/hooks/agentcell" />
              </label>
              <label className="field" style={{ flex: 1 }}>
                <span className="lbl">签名密钥 Secret</span>
                <input value={f.webhookSecret} onChange={set('webhookSecret')} placeholder="deploy-hmac" />
              </label>
            </div>
            <div className="hint" style={{ marginTop: -6 }}>
              请求体用 HMAC-SHA256 签名。<b>没有密钥就不会发</b> —— 一个谁拿到 URL 都能触发的部署,不算部署触发器。
            </div>
          </div>
        )}

        <div className="form-section-title">说明</div>
        <label className="field">
          <span className="lbl">产品描述</span>
          <textarea rows={3} value={f.description} onChange={set('description')} placeholder="这个产品是做什么的 —— 每次跟 agent 说话都会带上,可以随预览持续校准。" />
        </label>

        <details className="advanced">
          <summary>高级</summary>
          <div className="row" style={{ marginTop: 12 }}>
            <label className="field" style={{ flex: 1 }}>
              <span className="lbl">预览端口</span>
              <input value={f.previewPort} onChange={set('previewPort')} />
            </label>
            <label className="field" style={{ flex: 1 }}>
              <span className="lbl">并发槽位</span>
              <input value={f.maxSessions} onChange={set('maxSessions')} />
              <div className="hint">同时能开的会话数。常驻会话会一直占着槽位,直到你结束它。</div>
            </label>
          </div>
        </details>

        {create.error && <div className="form-error" style={{ marginTop: 14 }}>{(create.error as Error).message}</div>}

        <div className="btn-row" style={{ marginTop: 20 }}>
          <button className="primary" disabled={!ok || create.isPending} onClick={() => create.mutate()}>
            {create.isPending ? '创建中…' : '创建工作区'}
          </button>
          <button className="ghost" onClick={() => nav('/cells')}>
            取消
          </button>
        </div>
      </div>
    </>
  )
}
