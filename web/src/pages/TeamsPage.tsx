import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import type { Team } from '../api/types'
import { Badge, Spinner, useToast } from '../ui/primitives'
import { NONE } from '../lib/format'

/**
 * Teams: the membership list that outlives any one project.
 *
 * Per-Cell membership answers "who works on this" and nothing else a group
 * needs. Somebody joining does not join one project; somebody leaving must
 * not be removed from eleven of them by hand, because the twelfth is the one
 * that gets missed.
 */
export function TeamsPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [display, setDisplay] = useState('')

  const { data: teams, isLoading } = useQuery({ queryKey: ['teams'], queryFn: () => api.teams() })

  const create = useMutation({
    mutationFn: () => api.createTeam(name, display),
    onSuccess: () => {
      setName('')
      setDisplay('')
      toast.success('团队已建立,你是它的第一个 maintainer')
      qc.invalidateQueries({ queryKey: ['teams'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (isLoading) return <Spinner />

  return (
    <div className="stack">
      <div className="page-head">
        <h2>团队</h2>
        <p className="hint" style={{ margin: 0 }}>
          团队成员把角色带进它名下的每一个工作区。工作区里单独点名的人,以那里为准
          ——<b>既能提高也能降低</b>,这正是团队需要有的例外。
        </p>
      </div>

      {(teams ?? []).map((t) => (
        <TeamCard key={t.name} team={t} />
      ))}
      {(teams ?? []).length === 0 && (
        <div className="card">
          <p className="hint" style={{ margin: 0 }}>
            还没有团队。你只会看到自己所在的团队——一份完整的团队清单本身就是一张组织结构图。
          </p>
        </div>
      )}

      <div className="card">
        <h3>建一个团队</h3>
        <div className="row" style={{ marginTop: 8 }}>
          <input
            value={name}
            placeholder="id(小写字母、数字、短横,如 platform)"
            onChange={(e) => setName(e.target.value)}
          />
          <input value={display} placeholder="显示名(如 平台组)" onChange={(e) => setDisplay(e.target.value)} />
          <button disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>
            建立
          </button>
        </div>
      </div>
    </div>
  )
}

function TeamCard({ team }: { team: Team }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [uid, setUid] = useState('')
  const [role, setRole] = useState('member')
  const canEdit = team.role === 'maintainer'
  const done = () => qc.invalidateQueries({ queryKey: ['teams'] })

  const put = useMutation({
    mutationFn: (m: { id: string; role: string }) => api.putTeamMember(team.name, m.id, m.role),
    onSuccess: () => {
      setUid('')
      toast.success('成员已更新')
      done()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteTeamMember(team.name, id),
    onSuccess: () => {
      toast.success('已移出团队')
      done()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <h3 style={{ margin: 0 }}>
          {team.displayName || team.name} <span className="faint mono">{team.name}</span>
        </h3>
        <Badge tone={canEdit ? 'green' : 'gray'}>{team.role}</Badge>
      </div>

      {/* The blast radius, before the change rather than after it: removing
          somebody here removes them from every one of these projects. */}
      <p className="hint">
        管着{' '}
        {team.cells?.length ? (
          <b>{team.cells.join('、')}</b>
        ) : (
          <span className="faint">还没有工作区(在工作区设置里把它归到这个团队)</span>
        )}
      </p>

      <div className="table-wrap">
        <table className="data">
          <tbody>
            {(team.members ?? []).map((m) => (
              <tr key={m.userID}>
                <td className="mono">{m.userID}</td>
                <td style={{ width: 150 }}>
                  {canEdit ? (
                    <select value={m.role} onChange={(e) => put.mutate({ id: m.userID, role: e.target.value })}>
                      <option value="viewer">viewer</option>
                      <option value="member">member</option>
                      <option value="maintainer">maintainer</option>
                    </select>
                  ) : (
                    <span className="faint">{m.role}</span>
                  )}
                </td>
                <td style={{ width: 80 }}>
                  {canEdit && (
                    <button className="ghost small" onClick={() => remove.mutate(m.userID)}>
                      移除
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {(team.members ?? []).length === 0 && (
              <tr>
                <td className="faint">{NONE}</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {canEdit && (
        <div className="row" style={{ marginTop: 10 }}>
          <input
            value={uid}
            placeholder="用户 id(形如 u-1a2b3c4d,在对方账号里可见)"
            onChange={(e) => setUid(e.target.value)}
          />
          <select value={role} onChange={(e) => setRole(e.target.value)} style={{ width: 150 }}>
            <option value="viewer">viewer</option>
            <option value="member">member</option>
            <option value="maintainer">maintainer</option>
          </select>
          <button disabled={!uid.trim() || put.isPending} onClick={() => put.mutate({ id: uid.trim(), role })}>
            加入
          </button>
        </div>
      )}
    </div>
  )
}
