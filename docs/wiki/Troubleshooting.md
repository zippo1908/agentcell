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

1. **会话是不是休眠了?** 休眠会话的按钮是「唤醒并看终端」。
2. **唤醒卡住?** 唤醒要重新拿一个槽位;项目满了就得等。终端会把真实原因显示出来。
3. **只有 owner 能开终端**(团队会话除外)。不是你的会话,返回 404。

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
