import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { api } from '../api/client'
import { useToast } from '../ui/primitives'

/**
 * Onboarding a project used to require cellctl, which made the console a
 * viewer of projects it could never start.
 *
 * One column, one card, sections separated by hairline titles rather than
 * nested cards — the rest lives behind 高级 because most projects never
 * touch it.
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
    description: '',
    preview: '',
    previewPort: 3000,
    maxSessions: 2,
  })
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) =>
    setF({ ...f, [k]: e.target.value })

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
          <span className="lbl">
            名称<span className="req">*</span>
          </span>
          <input value={f.name} onChange={set('name')} placeholder="shop" />
          <div className="hint">小写字母、数字和连字符。会成为命名空间 cell-&lt;名称&gt; 和预览路径的一部分。</div>
        </label>
        <label className="field">
          <span className="lbl">
            仓库 URL<span className="req">*</span>
          </span>
          <input value={f.repoURL} onChange={set('repoURL')} placeholder="https://git.example.com/team/shop.git" />
        </label>
        <div className="row">
          <label className="field" style={{ flex: 1 }}>
            <span className="lbl">基线分支</span>
            <input value={f.branch} onChange={set('branch')} placeholder="main" />
          </label>
          <label className="field" style={{ flex: 1 }}>
            <span className="lbl">git 凭据 Secret</span>
            <input value={f.secretName} onChange={set('secretName')} placeholder="git-cred" />
          </label>
        </div>
        <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
          凭据由 git-broker 持有,任何工作负载都拿不到它。只能用你自己的 Secret。
        </div>

        <div className="form-section-title">运行环境</div>
        <label className="field">
          <span className="lbl">
            devbox 镜像<span className="req">*</span>
          </span>
          <input value={f.image} onChange={set('image')} placeholder="ghcr.io/zippo1908/devbox-e2e:v0.1.0-alpha.3" />
          <div className="hint">要包含 agent CLI、git,以及 tmux(常驻会话需要)。</div>
        </label>
        <label className="field">
          <span className="lbl">预览命令</span>
          <input value={f.preview} onChange={set('preview')} placeholder="npm run dev -- --host" />
          <div className="hint">留空则不起预览。它跑的是仓库里的代码,所以只在预览专用的 origin 上提供服务。</div>
        </label>

        <div className="form-section-title">说明</div>
        <label className="field">
          <span className="lbl">产品描述</span>
          <textarea rows={3} value={f.description} onChange={set('description')} placeholder="这个产品是做什么的 —— 每次派工都会带给 agent,可以随预览持续校准。" />
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
