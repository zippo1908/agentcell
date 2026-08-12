# ADR-0006: 批阅队列与自动 PR —— forge API 也经 broker

- 状态:已接受(2026-08)
- 关联:闭合 [ADR-0001](0001-architecture.md) 的 SDLC 环最后一段;沿用
  [ADR-0005](0005-git-broker.md) 的凭据集中原则

## 背景

今天 settle 会把有产出的会话推成 `session/<id>` 分支,但**到此为止**:没有
地方看 diff、没有批准/驳回、没有自动开 PR、不知道有没有合。README 的
"review → PR" 一直标着 ⬜。

要做这些就要调 forge REST API(比对 diff、开 PR、查 merge 状态),而调它需要
forge 凭据 —— 这引出一个必须先定的岔口。

## 决策 1:forge REST 也走 broker,celld 永不持有 forge 凭据

两个选项:

| | A. celld 直接读 git-cred 调 forge | B. 扩 broker,celld 经它调 forge REST |
|---|---|---|
| 实现 | 简单(几十行) | 多一层(broker 加 REST 代理路由) |
| 凭据面 | **celld 也持有 forge 令牌** | 令牌仍只在 broker |
| 与 ADR-0005 | 抵触(刚把令牌收敛到 broker) | 一致 |

**选 B。** 凭据集中在一个不跑仓库代码、可加固、可审计的组件里,是整套安全
模型的地基;为省几十行代码把令牌再撒一份到 celld,等于把 ADR-0005 做的事
抵消掉。broker 已经在做"验证调用者 → 注入真实凭据 → 转发"的事,forge REST
只是同一模式的第二条路由。

### 形态

```
celld (review queue)                 git-broker                    forge
  │  POST /api/cells/{cell}/review/{session}/approve
  ├── forge call ────────────────►  /forge/<cell>/<op>  ──────────►  REST API
  │   (celld's own SA token,          verify caller = celld SA
  │    audience-bound)                resolve cell → repo + creds
  │                                   allow-list ops only
  ◄── {url, number, state} ─────────  proxy response
```

- **调用者身份**:celld 用自己的 audience 绑定 SA 令牌;broker 校验它是控制
  面的 celld SA(不是任何 workload SA),再按 cell 解析仓库与凭据。
- **动作白名单**:broker 只放行有限操作 —— `compare`(diff)、`pull`
  (创建/查询 PR)。不做通用 forge 代理:一个能转发任意 forge API 的代理等于
  把 PAT 又交出去了。
- **PR 约束**:head 必须是 `session/<id>`,base 必须是 Cell 的 base 分支 ——
  与 ADR-0005 的 create-only 推送策略同源,防止用批阅通道做越权变更。

## 决策 2:批阅状态是 Session CR 的一等字段

不引入新 CRD、不引入数据库。`SessionStatus` 增加:

- `reviewState`: `Pending` | `Approved` | `Rejected`(仅当 `produced` 为真时
  有意义)
- `reviewNote`: 驳回原因 / 批准备注(驳回意见回填成新派工单的素材)
- `prURL` / `prNumber` / `prState`: `open` | `merged` | `closed`

理由:批阅是会话生命周期的一部分,不是独立实体;放在 CR 里天然获得
kubectl 可见性、RBAC、watch,和现有 reconcile 循环同一套语义。

## 决策 3:批准即开 PR,合并状态靠对账

- **approve** → Session controller 经 broker 开 PR,写回 `prURL/prNumber`,
  `reviewState=Approved`。

  **幂等按"查—建—回查"实现**(仅靠 `PRNumber==0` 跳过是不够的):创建前先
  `pull-find` 按 head 分支查已有 PR,有则**认领**;创建调用报错时**再查一次**
  ——因为"PR 已经开出去了但响应/状态写入丢了"是真实故障窗口,不回查就会对着
  一个回答 "already exists" 的 forge 无限重试,PR 明明存在平台却永远认不回来。
  认领成功时清掉此前的失败备注。
- **reject** → 记 `reviewState=Rejected` + `reviewNote`,不碰 forge。分支留在
  远端(证据不删),UI 提供"把驳回意见作为新任务派工"。
- **merge 跟踪** → 已开 PR 且未终态的 Session,由 reconciler 以退避周期查询
  `prState`;合并后 Session 进入终态展示,不再轮询。

## 后果

- **得**:SDLC 环闭合;凭据面不扩大;批阅状态可 kubectl 查、可 watch;PR 的
  head/base 受约束。
- **失/成本**:broker 增加一条 REST 路由与白名单(必须严格,否则退化为通用
  代理);forge 差异(GitHub / GitLab)进 broker 的适配层,首发 GitHub;
  merge 轮询有延迟(可接受,退避 + 终态停轮)。
- **未做**:行级评论、多人审批/法定人数、Cell 级审批策略 —— 后续按需。
