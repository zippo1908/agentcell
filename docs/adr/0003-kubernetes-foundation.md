# ADR-0003: Kubernetes 作为基础实现 —— Operator + CRD,私有云/云厂商二选一

- 状态:已接受(2026-08-08)。**部分取代** ADR-0001 的 D3(双进程特权分离)与 ADR-0002 的 D5(基础设施零感知)。
- 决策人:项目发起人(明确决定:K8s 是地基,不是未来选项)

## 背景

ADR-0001 最初选择 systemd+podman 单机形态,K8s 降级为未来驱动。发起人推翻该取舍:**K8s 必须是基础实现**——部署形态二选一:

1. **自带 K8s = 本地部署私有云**:用户在自己机房/服务器上装集群(单机用 k3s,一条命令),数据与模型流量全部不出内网;
2. **云服务商**:托管 K8s(阿里云 ACK、腾讯云 TKE 一等公民,亦兼容 EKS/GKE 等),可选用各家弹性能力(如 ECI/超级节点跑突发会话)。

单机顾虑(此前弃 K8s 的主因)由 **k3s 单节点**解决:安装脚本内置 `k3s + helm install agentcell` 快速路径,"一台干净机器五分钟跑起来"仍然成立。

## 决策

### D1 控制面 = 标准 Operator 模式

- 定义两个 CRD:**Cell** 与 **Session**(spec = 期望状态,status = 观测状态);
- `celld` 即 controller-manager:对账 Cell/Session CRD,原 Reconciler 直接映射为 controller 循环;
- **root 特权组件消失**:原 cell-provisionerd 的职责(建用户/目录/quadlet)由 K8s API 对象(Namespace/StatefulSet/Pod/PVC/Quota)取代;类型化契约铁律不变,只是契约语言从自定义 proto 换成 CRD schema。原 proto/UDS 设计仅在未来的 host-lite 驱动(后置,backlog)中保留。

### D2 资源映射

| AgentCell 概念 | K8s 资源 |
|---|---|
| Project / Cell | **Namespace**(隔离边界)+ **StatefulSet×1**(常驻锚点:git 对象库维护、发布/预览服务、PID1=cell-runtime)+ **PVC**(热态:主检出/缓存/agent-home) |
| Slot(槽位) | Cell CRD 的 `spec.maxSessions` + **ResourceQuota**(namespace 级总闸) |
| Session(会话) | **Pod**(挂同一 PVC,node 亲和到 Cell 锚点;`resources.limits` 即会话限额;内跑 tmux + worktree + agent) |
| Settle(清算) | Session CRD **finalizer**:删除前由 controller 跑清算(有产出推 `session/*` 分支→批阅队列;无产出丢弃 worktree;异常打包现场) |
| Reclaim | Pod 删除 + worktree 清理;槽位计数回落 |
| cell rebuild | 删除锚点 Pod + 重放 PVC 初始化 Job(或整 PVC 重建),声明式幂等 |
| 终端 attach | WebSocket → K8s exec API → session pod 内 `tmux attach` |
| 凭据 | 按会话:controller 派 Session 时注入 per-session **Secret**(agent 令牌);git/forge 令牌只在 broker(集群内独立 Deployment,Session pod 网络策略只许访问 broker,不给 forge 直连) |

存储:v0.1 用 node 本地 PV + 会话 pod 对 Cell 锚点的 podAffinity(单节点 k3s 天然满足);多节点集群可换 RWX StorageClass(NAS/CFS),这正是云厂商预置的用武之地。

### D3 部署形态与云厂商集成

- 交付物 = **容器镜像 + Helm chart**;operator 只说 vanilla K8s API,任何合规集群可跑;
- 云厂商差异收敛在 **Helm values 预置包**(`deploy/presets/`):`ack.yaml`(阿里云:云盘/NAS StorageClass、CLB ingress、可选 ECI 虚拟节点跑突发 Session)、`tke.yaml`(腾讯云:CBS/CFS、CLB、超级节点)、`k3s.yaml`(私有云单机快速路径)、`vanilla.yaml`;
- 模型云接入层(ADR-0002 D1–D4)不受影响,照旧:runner×provider 数据化预置。

### D4 安全模型换语言,不降级

每项目 unix user + subuid 的宿主中心模型,换成 K8s 原生表达:namespace 隔离 + Pod Security(restricted)+ 非 root 容器 + NetworkPolicy(Session pod 默认只通模型端点与 git broker)+ 可选 RuntimeClass(Kata/gVisor)做强隔离档。跨项目强隔离的语义目标不变。

## 后果

- **得**:多节点/弹性/自愈免费获得;"选云厂商"有了实体(托管 K8s + values 预置);控制面代码更少(K8s 替我们管进程、存储、重启);operator+CRD 是社区最熟悉的开源姿势,贡献者门槛低;
- **失**:派工延迟从 tmux 亚秒级变为 pod 启动秒级(热节点 1–2s,可接受);依赖预算 ≤12 放弃——k8s 驱动引 controller-runtime/client-go 全家桶(核心业务包仍保持依赖克制);cell-provisionerd 二进制从 v0.1 移除,host-lite 驱动进 backlog;
- 里程碑重排:M2=CRD+operator 骨架(kubebuilder/controller-runtime + envtest),M3=Cell 锚点(StatefulSet/PVC/镜像/k3s 开发环境),M4=Session pod 生命周期+finalizer 清算+槽位配额,M8 对账并入 controller 且加 chaos 验收,M10 增加 Helm chart 与 ACK/TKE 预置发布。
