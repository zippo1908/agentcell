# ADR-0005: git-broker — keep the forge token out of every workload pod

- 状态:**已实现(v1+v2+v3)**,2026-08。默认在 install.yaml 开启;直连模式保留为去掉 `--git-broker-url` 的回退。
- 关联:兑现 [ADR-0001](0001-architecture.md) 的"凭据不进容器"承诺中唯一未完成的一半;取代 [ADR-0004](0004-agent-sandbox-adoption.md) 无关

## 背景:欠账在哪

现状(v0.1.0-alpha.2):

- ✅ 会话 Pod 从不持有 forge 令牌;
- ✅ prod 服务容器零凭据(令牌只在 prod-clone init 容器);
- ✅ askpass 保证令牌不落 `.git/config`;
- ❌ **anchor / settle / prod-clone 仍通过 `GIT_USERNAME`/`GIT_TOKEN` 环境变量持有令牌**,而 anchor 同一个 Pod 里跑着仓库控制的预览 dev server。过滤子进程 env 挡住了直接继承,但 PID1 自身 env 里还有令牌。

AIP 的 `aip-git` 用宿主侧 broker + spool 文件解决过这个问题,但那依赖 provisionerd 与容器**共享宿主文件系统**——K8s 里跨 Pod 没有这个共享 FS,不能照搬。需要 K8s 原生的重新设计。

## 决策:一个"认证式 git 代理" broker

### 形态

`git-broker`:agentcell-system 里一个独立 Deployment(独立而非并入 celld,以隔离爆炸半径——它是唯一持有全部 forge 令牌的组件)。它是一个 **git smart-HTTP 反向代理**:

```
workload pod (anchor/settle/prod-clone)
    │  git clone/fetch/push  http://git-broker.agentcell-system.svc:8080/<cell>/…
    │  认证 = 该 Pod 的 projected ServiceAccount token(K8s 自动注入)
    ▼
git-broker (agentcell-system,唯一持令牌)
    │  1. 取请求里的 SA token → TokenReview → 得到 Pod 的 namespace = cell-<name>
    │  2. 校验 URL 里的 <cell> 与 namespace 一致(cell-foo 的 Pod 只能代表 cell-foo)
    │  3. 按 <cell> 读 Cell CR 拿真实 remote + 读 agentcell-system 里的 git Secret
    │  4. 注入真实 Authorization,反代到真实 forge,流式透传
    ▼
GitHub / GitLab(真实 remote,443)
```

**workload 从头到尾不持有 forge 令牌**——它只有自己的 SA token,而 SA token 在集群外一文不值,且只能让它对**自己这个 cell 的仓库**做 git 操作。

### 为什么用 SA token 而不是另发一个 broker 令牌

- SA token 由 K8s 自动投影到每个 Pod(`/var/run/secrets/kubernetes.io/serviceaccount/token`),**无需我们再造一套密钥分发/轮换**;
- 身份被密码学绑定到 Pod 的 namespace,而 **namespace 就是 cell 身份**——TokenReview 出来的 namespace 直接是授权依据,无法伪造;
- 泄露面缩小到极致:即便 anchor 里的恶意仓库代码读到 SA token,它能做的至多是"通过 broker 对本 cell 仓库做 git 操作"(它本来就在为这个仓库产出提交),**拿不到 PAT 本体、够不到其他仓库、用不了 PAT 的其他 scope**。而今天的 PAT 往往是宽 scope(all repos / admin),blast radius 天差地别。

### cell-runtime 侧改动(最小)

- 新增 env `AGENTCELL_GIT_BROKER=http://git-broker.agentcell-system.svc:8080`。**置位=broker 模式,不置位=直连模式(保留今天行为)**——broker 是可选但推荐,install.yaml 默认开启;
- broker 模式下:git 的 remote URL 重写为 `<broker>/<cell>`(真实 URL 不进 workload 的 git config,只有 cell 名);askpass 改为返回 **SA token 文件内容**(username 用固定 `x-access-token`),不再读 `GIT_TOKEN`;
- 控制器在 broker 模式下**不再把 `gitCredEnv` 注入 anchor/settle/prod-clone**——这三处的令牌 env 彻底消失。

### 授权边界(分阶段)

- **v1**:透明代理 + namespace↔cell 绑定校验。已经实现"令牌不进 workload + cell 只能碰自己的仓库"。
- **v2(动作级边界)**:broker 解析 `git-receive-pack` 的 pkt-line ref 更新,强制策略——只允许 push `session/*` 分支、禁 force-push、禁删分支、禁直推 base 分支。这正是 AIP "动作级边界"思路在 K8s 上的重生。
- **v3(短票据)**:broker 不再持长期 PAT,改为按操作向 forge 换取短期令牌(GitHub App installation token / GitLab job token)。forge 相关,后置。

## NetworkPolicy 变化(顺带收紧)

今天 Cell namespace egress 放 DNS+443。引入 broker 后:

- 新增 allow-egress 到 git-broker 的 svc:8080;
- **anchor/settle/prod 不再需要 443 egress**(git 走 broker,broker 才需要 443 到 forge)——可为这些 Pod 收掉通用 443;
- 会话 Pod 仍需 443(agent 要调模型 API),保持;
- broker 自身:ingress 只收 cell namespaces,egress 只放 DNS+443。

净效果:能碰仓库代码的 Pod,对公网 HTTPS 的出口进一步收窄。

## 后果

- **得**:兑现"令牌不进任何跑仓库代码的 Pod";PAT 泄露面从"每个 anchor"收敛到"一个无仓库代码、加固的 broker";namespace 即身份,零额外密钥分发;为动作级边界与短票据留好接口。
- **失/成本**:多一个必须部署的组件(可选,直连模式保底);git 操作多一跳(内网,可忽略);broker 是高价值目标,必须按 celld 同规格加固(非 root、只读根 FS、最小 RBAC:TokenReview + 读 Cell/Secret、无 shell)。
- **里程碑**:独立于当前功能线,作为一个"安全加固"批次;v1 可与现有直连模式并存,真机 e2e 增加一条 broker 模式跑通的断言后,再在 install.yaml 默认开启。

## 加固(v1.1,已实现)

在"令牌不进容器"之上做纵深防御,把 broker 的信任从"命名空间级"收紧到"角色+会话级":

1. **专用 ServiceAccount**:anchor/settle/prod 各用独立 SA(cell 命名空间内,零 RBAC),broker 据此区分角色——**只有 settle 角色能 push**,anchor/prod 只能 fetch。
2. **Session Pod 无令牌**:会话 Pod(跑不可信 agent+仓库代码)设 `automountServiceAccountToken: false`,根本不挂任何 SA token——它本就不碰 git,push 由 settle job 做。
3. **audience 绑定令牌**:workload 挂的是 audience=`agentcell-git-broker` 的**投影令牌**(1h TTL),不是默认 apiserver 令牌;broker 的 TokenReview 校验 audience,令牌无法跨用途重放。
4. **NetworkPolicy 按标签**:只有带 `broker-client` 标签的 Pod(anchor/settle/prod)有到 broker 的 egress;会话 Pod 无标签无令牌,双重够不到 broker。
5. **会话身份精确绑定**:settle 的 push ref 必须**恰好等于** `refs/heads/session/<session-id>`,其中 `<session-id>` 从 **settle Pod 名**(TokenReview 的 bound-token 声明,不可伪造)派生——一个 settle Pod 无法 push 到别的会话的分支,也无法碰 base 分支。
6. **repo↔凭据不可伪造绑定**:git Secret 可声明 `repo_url`;broker 校验 Cell 的 repo.url 与之一致——建 Cell 的人无法把某凭据配到另一个(如攻击者的)URL 上,也无法用别人的令牌配自己的 URL。

## 实现清单(v1)

1. `cmd/git-broker`:smart-HTTP 反代 + TokenReview 鉴权 + namespace↔cell 校验 + Cell/Secret 查询;
2. `config/install.yaml`:git-broker Deployment/Service/SA/RBAC(TokenReview、读 Cell、读 git Secret),加固同 celld;
3. `pkg/runtimeapi`:`EnvGitBroker` 常量 + SA token 路径常量;
4. `cmd/cell-runtime`:broker 模式的 URL 重写 + askpass 读 SA token;
5. `internal/controller`:broker 模式下改注 broker URL、去掉 gitCredEnv;NetworkPolicy 加 broker egress;
6. 测试:broker 鉴权单测(TokenReview mock:对的 namespace 放行、错的 cell 拒绝)、控制器断言 broker 模式下 workload 无令牌 env;e2e 加 broker 模式。
