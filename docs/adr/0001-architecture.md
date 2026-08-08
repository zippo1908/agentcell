# ADR-0001: 总体架构 —— 常驻 Cell + 会话槽位 + 双进程特权分离

- 状态:已接受(2026-08);**D3 被 ADR-0003 部分取代**(基础实现改为 K8s Operator,root provisionerd 从 v0.1 移除;D1/D2/D4 的语义不变,资源映射见 ADR-0003 D2)
- 决策人:项目发起人

## 背景

AI 编码 agent 的执行环境有两个既有流派,都不合适:

1. **一次性沙盒**(E2B/Daytona 式):每个任务冷启动 provision(建用户/容器/克隆/装依赖),派工延迟高,环境状态无法跨任务积累;
2. **人用 workspace**(Coder/Gitpod 式):环境常驻但不管理 agent——没有槽位、限额、回收、批阅的概念。

内部前身平台(闭源)验证了"一项目一容器一 agent"模型可行,但把 tmux/agent 状态做成了项目级标量,并发能力和回收语义都是补丁。

## 决策

### D1 常驻 Cell 与一次性 Slot 分层

- **Cell**(项目 1:1):专属 unix user + subuid 段、rootless podman 容器(quadlet 常驻)、静态 PID1(`cell-runtime`)、主检出作 git 对象库。宠物但可重建:`cell rebuild` 幂等回干净态。
- **Slot**(会话占用,一次性):git worktree + tmux 会话 + cgroup v2 子树(quadlet 开委托、PID1 管理 `cpu.max`/`memory.max`)+ 按会话注入的凭据。
- 生命周期 `dispatch → work → settle → reclaim`;**settle 强制**:有产出推 `session/<id>` 分支入批阅队列,无产出丢弃,异常打包现场再清。

### D2 沙盒与实例合并为一层

不存在独立的"沙盒"概念:容器即 Cell。保留 Cell 级驱动抽象(默认 `podman`;可选 `host` 裸跑驱动 = unix user + systemd 沙盒指令 + cgroup,用于完全信任场景),默认永远是容器。理由:常驻化摊销了容器唯一的成本(冷启动),而网络边界可配置(宿主环回隧道是否可见)、容器内 root(agent 自装依赖)、可重建性三项收益只有容器给得了。

### D3 双进程特权分离

- `celld`(非 root):API、认证、注册表、批阅队列、Reconciler、SQLite。
- `cell-provisionerd`(唯一 root):group 受限 UDS 上的类型化 gRPC。**铁律:RPC 只收 id 与类型化配置,永不收命令串、永不收调用方指定的宿主路径**;宿主资源名一律由 id 在单一命名包(M1 `pkg/ids`)派生。
- git/forge 令牌只存在于宿主 broker(provisionerd 内的 spool 轮询),容器内只有写请求文件的假 git 助手。

### D4 逐 Slot 对账

Reconciler 的漂移判据是 per-slot 四维(容器/tmux/cgroup/心跳),不是项目级标量;会话失联走 settle 而非重建整个 Cell。

## 后果

- 派工延迟 ≈ worktree add + tmux new-session(亚秒级),对比一次性沙盒的分钟级 provision;
- 代价:Cell 是有状态服务,必须配套 rebuild 纪律(发布后滚动重建)控制漂移;
- 多会话共享 Cell 文件系统,worktree 隔离的是工作区,不隔离全局工具链——同项目内会话互信是模型假设(跨项目仍强隔离)。
