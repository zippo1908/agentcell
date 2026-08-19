# 排障

**先问一句:这是谁的状态?** 大多数难查的问题都是"平台其实已经知道原因,只是没
把它送到你眼前"。

## 工作区一直 Pending

```sh
kubectl -n agentcell-system get cell <name> -o jsonpath='{.status.node} {.status.schedulingMessage}'
```

`schedulingMessage` 是**调度器的原话**。最常见的是选择器匹配不到机器,或者机器
没地方了。控制台的设置页也会把这句显示出来。

## 终端打不开

按这个顺序看:

1. **你有没有会话?** 终端只对**已存在的常驻会话**开放。新账号第一次进项目时,
   要先由「打开终端 / 派一单」把会话建出来。
2. **建会话被凭据挡住了?** 这是新同事最常撞的一堵墙,而且报错说的是凭据、
   不是终端:

   > 你还没有配模型 key——去「我的凭据」加一个再来。

   派工在**取项目之前**就要求调用者自己有凭据:连过 Kimi 账号,或者有一把模型
   key。别人的凭据不会借给你——这是「按人认、按人记账」的直接后果,不是故障。
3. **会话是不是休眠了?** 休眠会话的按钮是「唤醒并看终端」。
4. **唤醒卡住?** 唤醒要重新拿一个槽位;项目满了就得等。终端会把真实原因显示出来。
5. **只有 owner 能开终端**(黑板会话除外)。不是你的会话,返回 404。

## 预览一直是空的

先看 anchor 的日志——它会把自己的判断写出来:

```sh
kubectl -n cell-<name> logs sts/anchor -c anchor | tail -20
```

三种可能:

- `anchor: 自动判定预览命令 [...]` —— 判出来了,但服务没起来。看它后面的报错,
  常见是绑到了 loopback,或者绑到了自己的默认端口而不是 `preview-port`。
- `anchor: 不启动预览 — 这个仓库里没有可识别的启动方式…` —— 判不出来。给这个项目
  显式写一条预览命令即可(显式的永远优先)。
- 什么都没有 —— 检查 `spec.preview.mode` 是不是 `off`。

**anchor 长期 NotReady 而端口没人绑**:只有**显式写下的**命令才会被 readiness
探针盯着(自动模式不会,因为它没法承诺一定有预览)。alpha.7 之前,单元素命令会被
anchor 当成文件名去 exec —— 升级,或者把命令拆成真正的 argv。详见
[ADR-0014](../adr/0014-preview-without-a-command.md)。

## 会话总是很快就睡着

`idleSeconds` 默认 15 分钟,而且**只有"没有 agent 在跑、也没有人在看"才算闲**。
如果它在你眼皮底下睡了,多半是窗口状态没被观测到——看:

```sh
kubectl -n cell-<name> exec runtime-<uid> -- /agentcell/cell-runtime window-status <session-id>
# alive=true exit=0 attached=true
```

## agent 说自己是别的模型 / 一直重连

CLI 的 provider 没被采纳。**Codex 从配置文件读端点,不从 `OPENAI_BASE_URL` 读**——
平台会把 `config.toml` 写进会话自己的 `CODEX_HOME`。确认:

```sh
kubectl -n cell-<name> exec runtime-<uid> -- cat /workspace/users/<uid>/state/<id>/config.toml
```

日志第一行会写 `model:` 和 `provider:`,和你派的不一样就是没生效。

## 镜像改了没生效

见 [镜像与内网](Images):`IfNotPresent` + 覆盖同名 tag = 节点一直跑旧的,而且不报错。

## 控制台 401 / 看不到东西

- 浏览器访问是 303 跳登录页;**API 才返回 401**。
- 看不见某个项目通常是**权限**,不是 bug:不属于你的返回 404 而不是 403,这是故意的。

---

<details>
<summary><b>English</b> — troubleshooting</summary>

**First question: whose state is this?** Most hard-to-find problems here are
"the platform already knows why, but did not put it in front of you".

- **Project stuck Pending** — read `status.schedulingMessage`. It is the
  scheduler's own words, usually "no machine matches" or "no room".
- **Terminal will not open** — is the session asleep (the button then says
  "wake and open")? Is the wake waiting for a slot? Only the owner can open a
  personal session; anything else answers 404.
- **Sessions sleep too fast** — idle means no agent running *and* nobody
  watching. Check `cell-runtime window-status <id>`.
- **The agent says it is a different model, or keeps reconnecting** — its
  provider was not applied. Codex reads its endpoint from a config file, not
  from `OPENAI_BASE_URL`; the platform writes one into the session's own
  `CODEX_HOME`. The first log line prints the model and provider it actually
  used.
- **An image change did nothing** — see Images: `IfNotPresent` plus an
  overwritten tag means the node keeps the old one, silently.
- **401 from the console** — browsers get a redirect to the login page; only
  the API returns 401. Not seeing a project is usually permissions, not a
  bug: what is not yours answers 404 on purpose.

</details>

## 「账号库暂时读不到,平台级操作已停用」

**这不是故障,是设计。** 授权在控制面读不到账号库时一律关门
([ADR-0015](../adr/0015-authorization-fails-closed.md)):控制面不可用可以让工作
停下,但不能让权限变宽。故障期间**管理员也一并被拒绝**——管理员身份恰恰是此刻
无法核验的那个声明。

受影响的只有平台级操作(建项目、邀请人)。开终端、派活、看预览不经过这条路。

**怎么进去修**:用**部署令牌**。它在查库之前被解析,所以库挂了它照常工作——
这是唯一的逃生通道,而且是有意做窄的。

```bash
kubectl -n agentcell-system get secret celld-tokens -o jsonpath='{.data}'
```

**怎么确认真因**:看 celld 日志里判定的 `rule`。`store-unavailable` 是库读不到;
`account-deleted` 是这个账号已经被删了而 cookie 还没过期——后者不是故障,不用修。
