<div align="right">

[English](README.md) · **中文**

</div>

# AgentCell

**AI 开发员工的车间。** 每个项目一个常驻的、跑在 Kubernetes 上的实例(*Cell*);
会话是实例内的一次性工位(Slot);自带常驻产品预览供你边看边校准;SDLC 环——
派工 → 干活 → 清算 → 批阅 → 发布——在实例内闭合。

> **状态:alpha。** 全链路已在真实单节点 k3s 上通过 8 步 e2e(鉴权 → reconcile →
> 预览 → 派工 → 清算 → 推分支 → 发布 → 正式区,[e2e 结果](docs/E2E_RESULTS.md))。
> 批阅/PR 审批队列尚在路线图。用于生产前请先评估——下方表格如实列出"已实现 vs
> 设计目标"。

## 为什么

- **常驻 Cell + 一次性 Slot。** 一次性沙盒每单交冷启动税,人用的 workspace 又不管
  agent。AgentCell 让项目环境保持热态,每个会话独占自己的 git worktree,常驻会话共享属主 runtime 的资源上限,下线
  走**清算**:有产出推 `session/<id>` 分支,空会话丢弃,不留垃圾。
- **边看边校准。** 每个 Cell 常驻跑产品 dev server;UI 左边是产品描述,右边是实时
  预览——你对着 agent 正在做出来的东西随手校准。
- **凭据够不到。** 模型 key 按会话注入。开启 git-broker(默认)后**没有任何 workload
  pod 持有 forge 令牌**——pod 用 audience 绑定的 ServiceAccount 令牌认证;只有 settle
  角色能 push,且只能 create-only 推自己那一个分支。项目预览是仓库与 agent 写的代码,
  因此**按 Cell 与区各自独立 origin** 运行,代理在请求到达它之前剥掉所有平台凭据——
  而预览应用对自己仍是完整同源,毫无退化。
- **国内云友好。** 服务商是数据不是代码:阿里百炼、腾讯混元、DeepSeek、Kimi、智谱
  经其 OpenAI/Anthropic 兼容端点开箱即用,免代理。

## 核心模型

**项目是共享的,个人的运行时不是。** 协作发生在项目层——分支、批阅、知识库,
而不是进程层。任何人都不会 attach 到别人的终端上。

```mermaid
flowchart TB
    subgraph CELL["Cell — 一个项目"]
        OBJ[("/workspace/repo · knowledge<br/>共享,归项目所有")]
        ANCHOR["anchor — 克隆 · 基线预览"]
        subgraph UA["Alice · uid 100000 · 0700"]
            TA["一个 tmux server"]
            WA1["window:会话 a1"]
            WA2["window:会话 a2"]
        end
        subgraph UB["Bob · uid 100001 · 0700"]
            TB["一个 tmux server"]
            WB1["window:会话 b1"]
        end
    end
    TA --> WA1 & WA2
    TB --> WB1
    WA1 & WA2 & WB1 -.->|读| OBJ
    WA1 -->|清算 · 唯一出口| BR["session/&lt;id&gt; → 批阅 → PR → 发布"]
    WB1 -->|清算| BR
```

**一个用户一个 tmux,而不是一个会话一个**:agent CLI 自己管理对话(Claude Code
用我们指定的 id,Codex 用它自己的),所以平台只负责给它们一个私有 `$HOME` 存这些
状态、一个比任何单次运行都活得久的终端,其余不插手。

清算是进入项目层的唯一一道门。worktree 可以由属主决定留多久,但任何东西不经过
清算都到不了分支上。

完整图(控制面、生命周期、git-broker):**[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**。

## 已实现 vs 设计目标

| 能力 | 状态 |
|---|---|
| Cell 控制器(命名空间/PVC/锚点/预览)· 会话生命周期(槽位闸 → settle Job → 回收) | ✅ 有测试 |
| settle 数据安全(推送确认否则重试、永不删未送达 worktree) | ✅ 真 git 测试 |
| 常驻预览 + 校准 UI(React SPA,内嵌进 celld)· 双区发布(分支/标签/SHA、回滚) | ✅ |
| **不可信内容按 origin 隔离**:每个 Cell 的每个区独立 host、一次性票据、平台凭据永不到达仓库代码 | ✅ 有测试([ADR-0007](docs/adr/0007-preview-origin-separation.md)) |
| 服务商注册表(阿里百炼 / 腾讯混元 / DeepSeek / …) | ✅ 有测试 |
| HTTP 鉴权(Bearer + 登录 cookie,默认拒绝裸启)· 每 Cell NetworkPolicy · PSS restricted · 非 root Pod | ✅ |
| 无竞态槽位租约(含崩溃恢复) | ✅ 有测试 |
| **git-broker**:令牌不进任何 workload pod;按角色 SA;audience 绑定令牌;repo↔凭据绑定;push 经 pod uid+owner 校验;`session/<id>` create-only | ✅ 有测试([ADR-0005](docs/adr/0005-git-broker.md)) |
| 真机 k3s e2e — 全 8 步含预览与正式区 HTTP 200 | ✅([Run 3](docs/E2E_RESULTS.md)) |
| 批阅队列 · diff · 通过即开 PR · merge 跟踪(forge API 经 broker,celld 不持凭据) | ✅ 有测试([ADR-0006](docs/adr/0006-review-queue-and-pr.md)) |
| Helm chart + GHCR 镜像 + 云预置(k3s / ACK / TKE) | ✅ 经 `helm lint` 验证 |
| 自建 GitLab 作为一等 forge(compare / 开 MR / 跟踪) | ✅ 实测([Run 4](docs/E2E_RESULTS.md)) |
| 私有 registry:pull secret 复制进每个 Cell 命名空间 | ✅ 实测 |
| **用户身份**:celld 自验 OIDC(不信任身份头、不强依赖网关);Session 属主不可变;越权一律 404 | ✅ 实测([ADR-0008](docs/adr/0008-user-identity-and-ownership.md)) |
| 注册登录用 Casdoor、边缘用 Apache APISIX®,二者**均可选**,任何 OIDC 提供方都能用 | ✅ 清单([deploy/identity](deploy/identity/)) |
| **运行时隔离**:每用户独立 Unix uid(分配且永不复用)、`0700` 私有目录(worktree / `$HOME` / CLI 状态 / tmux socket) | ✅ 实测([Run 5](docs/E2E_RESULTS.md)、[ADR-0009](docs/adr/0009-runtime-isolation.md)) |
| **常驻会话**:agent 结束后槽位仍在,追加指令续的是 CLI 自己的对话,清算仍然强制 | ✅ 实测([Run 6](docs/E2E_RESULTS.md)、[ADR-0010](docs/adr/0010-resident-sessions.md)) |
| 每用户一个 tmux runtime 承载多个会话(window);模型 key 绝不进 argv | ✅ 实测([Run 7](docs/E2E_RESULTS.md)) |
| **runner 也是数据**:加一个 CLI 或修一个 flag 只改 `runners.d/*.yaml`,不用发版([docs/RUNNERS.md](docs/RUNNERS.md)) | ✅ 实测 |
| 派工表单由服务端目录驱动:选 runner 只列它能驱动的 provider、默认同厂商、模型来自清单且可自填 | ✅ |
| 浏览器内终端(tmux over WebSocket)——现在可用 `kubectl exec … cell-runtime attach <id>` | ⬜ 设计中(M5) |
| **不靠 git 代理做隔离**:每用户独立仓库 + 共享只读基底,未发布提交进不了共享对象库 | ✅ 实测([Run 8](docs/E2E_RESULTS.md)、[ADR-0012](docs/adr/0012-git-isolation-decision.md)) |
| **正式区可外置**:Cell 内隔离正式区,或把发布交给真正在跑它的系统(签名 webhook) | ✅ 实测 |
| 容量:所有负载声明 requests/limits,每个 Cell 有 ResourceQuota。注意常驻会话是 tmux window,与同一 runtime 内其他窗口共享额度——K8s 按 pod 预留,而 window 不是 pod | ✅ 实测([Run 8](docs/E2E_RESULTS.md)) |
| runtime pod 被替换后续上原对话(窗口会恢复,但重新接上 CLI 对话未接) | ⬜ 设计中 |
| agent-sandbox 底座 · 多节点 RWX | ⬜ 设计中 |

## 一条命令安装

镜像与 chart 都发布在 GHCR,集群能联网就无需本地构建:

```sh
helm install agentcell oci://ghcr.io/zippo1908/charts/agentcell \
  --namespace agentcell-system --create-namespace \
  --set celld.auth.tokens="{$(openssl rand -hex 24)}" \
  --set preview.domain=preview.example.com --set preview.ingress.enabled=true
```

`preview.domain` 让每个 Cell 的每个区拥有独立 host(`<cell>-dev.…`、
`<cell>-prod.…`),需要泛域名解析与泛证书。**仓库并非完全可信的部署必须设置**
——不设则所有 Cell 共用一个预览 origin(见
[ADR-0007](docs/adr/0007-preview-origin-separation.md))。

k3s / 阿里 ACK / 腾讯 TKE 预置:`-f deploy/presets/<名字>.yaml`。装完按 chart
输出的提示创建 git 凭据、模型 key 和第一个 Cell。完整说明见
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)。

## 从源码构建

```sh
# 1. 构建镜像并导入集群(单机走 k3s)
make build build-runtime-static
podman build -t ghcr.io/agentcell/celld      -f images/celld/Containerfile .
podman build -t ghcr.io/agentcell/git-broker -f images/git-broker/Containerfile .
podman build -t ghcr.io/agentcell/devbox     -f images/devbox/Containerfile .
for i in celld git-broker devbox; do podman save ghcr.io/agentcell/$i | sudo k3s ctr images import -; done

# 2. 装控制面(k3s: curl -sfL https://get.k3s.io | INSTALL_K3S_MIRROR=cn sh -)
kubectl apply -f config/crd/ -f config/install.yaml

# 3. Secret:API 令牌、git 凭据(绑定到其仓库)、模型 key
kubectl -n agentcell-system create secret generic celld-tokens --from-literal=tokens="$(openssl rand -hex 24)"
kubectl -n agentcell-system create secret generic git-cred --type=kubernetes.io/basic-auth \
  --from-literal=username=bot --from-literal=password=ghp_... \
  --from-literal=repo_url=https://github.com/you/shop.git
kubectl -n agentcell-system create secret generic bailian-key --from-literal=key=sk-...
kubectl -n agentcell-system rollout restart deploy/celld

# 4. 建带常驻预览的 Cell,派工并实时看
cellctl cell create shop --repo https://github.com/you/shop.git \
  --image ghcr.io/agentcell/devbox --secret git-cred \
  --preview "npm run dev -- --host" --preview-port 5173 --description "极简电商"
cellctl dispatch shop --task "把商品卡片改成两列" \
  --runner claude --provider aliyun-bailian --model qwen3-coder-plus --cred bailian-key --follow

# 5. 打开 UI
kubectl -n agentcell-system port-forward svc/celld 8080:80   # http://localhost:8080,用令牌登录
```

生产部署(Ingress/TLS、存储、升级、排障):**[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**。

## 多人使用

开箱只有一个主体:持有令牌的人。接上 OIDC 之后,每个人有自己的归属、uid 和运行时:

```sh
helm upgrade --install agentcell oci://ghcr.io/zippo1908/charts/agentcell \
  --set oidc.issuer=https://casdoor.example.com --set oidc.clientID=... \
  --set oidc.existingSecret=oidc
```

celld **自己**拿 provider 的 JWKS 验 ID token,**绝不信任身份头**——Pod 网络上任何
东西都能伪造一个头。所以不装网关也能用,任何标准 OIDC 提供方都行。想要现成的注册、
登录和 TLS,[`deploy/identity/`](deploy/identity/) 里有 Casdoor + Apache APISIX® 的清单。

**边看边改**:常驻会话在 agent 结束后保留槽位,你可以看完结果**在同一个对话里再补
一句**,而不是重派一个什么都要重新摸清的新会话:

```sh
cellctl dispatch shop --task "把商品卡片改成两列" --resident ...
curl -H "Authorization: Bearer $TOKEN" .../api/sessions/$S/state    # 在跑?跑完了?退出码?
curl -X POST ... -d '{"text":"两列太挤了,间距加大一点"}' .../api/sessions/$S/continue
kubectl -n cell-shop exec -it runtime-100000 -- /agentcell/cell-runtime attach $ID
curl -X DELETE ... .../api/sessions/$S                              # 清算:提交、推送、进批阅
```

你在一个 Cell 里的每个会话,都是**你自己** runtime 里的一个 window,tmux socket 只有
你的 uid 能打开。别人正在跑的会话,在他清算之前**在控制台上完全看不到**——这是 API 层
的归属过滤;有集群权限的人当然能直接看 CR,那是另一个层级的授权问题。

## 组件

- **`celld`** —— `Cell`/`Session` CRD 的 operator,外加两个 HTTP 面:控制台
  (React SPA + API,`:8080`)与**独立 origin** 上的不可信内容代理(`:8081`)。
  前端在 `web/`,用 `go:embed` 打进二进制——仍是单二进制。
- **`git-broker`** —— 唯一持有 forge 凭据的组件,一个认证式 git 代理。
- **`cell-runtime`** —— 静态 multi-call 二进制,anchor/session/prod pod 的 PID 1。
- **`cellctl`** —— 运维 CLI。

部署在任意合规 Kubernetes:自带(单机 k3s = 本地私有云)或云厂商托管(阿里 ACK /
腾讯 TKE),见 [ADR-0003](docs/adr/0003-kubernetes-foundation.md)。

## 文档

[架构](docs/ARCHITECTURE.md) · [部署](docs/DEPLOYMENT.md) ·
[路线图](docs/ROADMAP.md) · [ADR](docs/adr/) ·
[贡献](CONTRIBUTING.md) · [安全](SECURITY.md) ·
[good first issues](https://github.com/zippo1908/agentcell/labels/good%20first%20issue)

## 许可

Apache-2.0,见 [LICENSE](LICENSE)。
