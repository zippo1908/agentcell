# ADR-0004: 采用 kubernetes-sigs/agent-sandbox 作为 Cell 底座(分两阶段)

- 状态:已接受方向,Phase 1 待真机验证(2026-08-09 评估)
- 评估对象:https://github.com/kubernetes-sigs/agent-sandbox @ e6d10fe(评估前一日仍有提交,项目高度活跃)

## 评估结论(基于源码而非博客)

**成熟度高于预期:**
- API 已升 **v1beta1**(group `agents.x-k8s.io`),不是传闻中的早期 alpha;带 CEL 校验、printer columns、OLM/Helm 发布物、SECURITY.md、KEP 流程;
- `Sandbox` = podTemplate + **volumeClaimTemplates(不可变)** + 可选 headless Service(status 回写 `serviceFQDN`)+ `shutdownTime` 定时关停 —— 有状态单例语义完整,**支持挂 PVC 正中我们的 workspace 需求**;
- 扩展件:`SandboxTemplate` / `SandboxClaim` / `SandboxWarmPool`(预热池领取,路线图延迟目标 200ms→100ms→50ms);
- 周边:`sandboxd`(沙盒内 gRPC daemon:文件/进程 API)、`sandbox-router`、Python/TS/Go SDK、规划中的 MCP server;
- 路线图与我们强相关:Auto Suspend/Resume、Scale-to-Zero、Multi-Sandbox-per-Pod、运行时后端解耦(microVM)。

**与 AgentCell 的重叠与互补:**

| 我们手写的 | 它提供的 | 判断 |
|---|---|---|
| Cell 锚点 StatefulSet + preview Service | `Sandbox`(podTemplate+PVC+Service+FQDN) | **重叠,应替换** |
| 无(派工冷启动) | `SandboxClaim`+`WarmPool` 预热领取 | **互补,想要** |
| 无(闲置 Cell 白烧资源) | `shutdownTime` / 规划中的 scale-to-zero | **互补,免费拿** |
| cell-runtime(worktree/settle/任务注入) | `sandboxd`(通用文件/进程 API) | 不冲突,我们的是业务语义 |
| celld 的 /preview /app 反代 | sandbox-router | 暂不采用,避免多引一件 |
| Session 生命周期/settle/批阅/双区/知识/接入层 | 无 | **我们的全部价值,保留** |

## 决策

### Phase 1(并入 M3 重构):锚点跑在 Sandbox 上,驱动缝隔离

- Cell 控制器增加驱动缝:`native`(现 StatefulSet 实现,保底)与 `sandbox`(渲染 `Sandbox` CR:anchor podTemplate + workspace volumeClaimTemplate + service: true);
- 预览地址从我们的 Service 名改读 `status.serviceFQDN`;
- **顺手修一个现有设计弱点**:预览跟随目标从"改 StatefulSet env→滚 Pod"改为"写 PVC 上的 target 文件,anchor 监视并重启 dev server"——不再依赖 Pod 模板可变性(Sandbox 的 podTemplate 更新语义尚在演进),切换预览也从"滚 Pod 几秒"变成"进程内秒切";
- 版本 pin 到其 release,controller 以 Helm 子 chart 方式随我们安装;native 驱动保留至 Phase 1 真机跑通后一个版本再废弃。

### Phase 2(M4 之后评估,不阻塞):会话走 WarmPool

会话改 `SandboxClaim` 从预热池领取,可把派工延迟压到亚秒且环境预装。**前提是放弃"共享 PVC 的 worktree"模型**(Claim 出来的沙盒有自己的卷),改为"从锚点浅克隆 + push 回"或等其 Multi-Sandbox-per-Pod 落地。收益大、改动深,单独立项评估,不与 Phase 1 捆绑。

### 不采用的部分

sandboxd / sandbox-router / SDK:与 celld 反代和 cell-runtime 职责重叠,引入只增依赖面;等我们需要"平台外程序直接操作沙盒"时再议。

## 风险

- v1beta1 仍可能破坏性演进 → 驱动缝 + 版本 pin + native 保底;
- 多一个 controller 的安装与升级面 → Helm 子 chart 化,随平台一次装;
- 该项目由大厂主导节奏(OpenClaw 价格性能目标等)→ 我们只消费稳定 API 面(Sandbox/Template/Claim),不绑其实验特性。

## 对外叙事(定稿)

> **沙盒层用 Kubernetes 官方标准件,AgentCell 只写它之上的 SDLC 控制层:常驻项目上下文、per-task worktree、强制 settle、批阅/双区发布、知识回流、国内模型接入。**
