# 权限

## 三个角色,八个动作

| 角色 | 能做 |
|---|---|
| `viewer` | 看项目、已清算的会话、批阅 |
| `member` | 加上派工、跑会话、批阅 |
| `maintainer` | 加上发布、改设置、管成员 |

**发布是那条要紧的线。** 其他都可恢复;发布是唯一一个把代码摆到用户面前的动作。

## 团队与项目的关系

**项目上点了名的以项目为准,否则用团队的角色。而且覆盖是双向的**——既能提高也能
降低。取两者中较高的看起来大方,实则会让"这个人在这一个项目上只是 viewer"变得
无法表达,而那恰恰是团队需要有的例外。

**归属团队会让项目变成按成员授权。** 属于某个组的项目,不该同时对所有能登录的人开放。

## 有些边界不是角色能跨的

**终端**只有会话的 owner 能开,项目的 maintainer 也不行。socket 在用户私有目录里,
**它本身就是权限**;能看项目 ≠ 能拿别人的键盘。(团队会话除外——那段对话是团队的。)

**机器池**由集群管理员定义,项目 maintainer 只能在清单里挑。这条尤其重要:
**maintainer 是项目角色,不是集群管理员**,而污点是管理员的拒绝——读到它再自动写上
对应的容忍,不是"放置功能",是绕过。所以 `PUT /placement` **只接受一个 class 名字**,
你送来的任何东西都不会变成节点选择器或 toleration。

## 为什么是 404 不是 403

看不见的项目返回 404,而不是 403。403 等于确认"这个项目存在,而你在外面"——探几次
就能把一个团队在做什么摸清楚。**只有在你本来就看得见的项目上做不了的动作**,才返回
403 并说明需要什么角色,这样你才知道该找谁要权限。

---

<details>
<summary><b>English</b> — permissions</summary>

Three roles: `viewer` sees, `member` also dispatches and reviews,
`maintainer` also releases and changes settings. **Release is the line that
matters** — everything else is recoverable; a release is the one action that
puts code in front of users.

**Teams give defaults; a project can override them in both directions.** An
entry naming you on the project wins, whether it raises your role or lowers
it — otherwise "a viewer on this one project" would be unsayable, and that is
exactly the exception a team needs to have.

**Some boundaries are not role-shaped.** Only a session's owner can open its
terminal — not even a project maintainer — because the socket lives in that
person's private area and seeing a project is not the same as holding
somebody's keyboard. (The team's own board session is the exception: that
conversation belongs to the team.)

**Machine pools are defined by cluster administrators**, and a project
maintainer can only choose from the list. This one matters: a maintainer is
not an administrator, and a taint is an administrator's refusal — reading one
and writing the matching toleration would be a bypass, not a feature.

**Why 404 and not 403:** a 403 confirms that a project exists and that you are
outside it, which over a few probes maps out what a team is working on. You
get 403 only for actions on projects you can already see, and it names the
role you would need — so you know who to ask.

</details>
