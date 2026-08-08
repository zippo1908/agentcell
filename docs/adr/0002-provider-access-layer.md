# ADR-0002: 模型服务接入层 —— runner × provider 二维注册表,国内云一等公民

- 状态:已接受(2026-08)
- 决策人:项目发起人(明确要求:阿里云、腾讯云接入必须友好)

## 背景

平台不内嵌任何模型 SDK——agent 是外部进程(claude / codex / pi 等 CLI)。但这些 CLI 各自默认只认自家 API,而目标用户(尤其国内自托管用户)实际使用的模型服务高度多样:阿里云百炼(通义千问)、腾讯混元、DeepSeek、月之暗面 Kimi、智谱 GLM……它们几乎都通过 **OpenAI 兼容**和/或 **Anthropic 兼容**端点提供服务。接入层设计得不好,每种"CLI × 云"组合都要写代码,矩阵爆炸。

## 决策

### D1 runner 与 provider 是两个正交维度

- **runner**:会写代码的 CLI(claude / codex / pi / 未来者)。每个 runner 声明:①启动 argv;②凭据注入方式(env 或文件);③headless 派工方式;④会话文件路径解析;⑤权限旁路参数;⑥**它会说哪些协议**(claude→anthropic;codex→openai;pi→两者)。
- **provider**:模型服务(anthropic / openai / aliyun-bailian / tencent-hunyuan / deepseek / moonshot / zhipu / openrouter / 自定义)。每个 provider 声明:**它以哪些协议供货**,每协议一个 base_url + 认证 env 名 + 可选模型清单。
- 一个绑定 `(runner, provider, model)` 合法,当且仅当两者协议集合有交集。平台据此自动生成注入的 env(如 `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_MODEL`,或 `OPENAI_BASE_URL`/`OPENAI_API_KEY`)。

### D2 预置是数据,不是代码

Provider 预置放 `configs/providers.yaml`(编译期 embed),用户可在 `/etc/agentcell/providers.d/*.yaml` 覆盖或新增——**接一朵新云 = 加一段 yaml,零代码**。base_url、env 名、协议路径这类会随官方文档变化的字段,一律以用户覆盖优先。

### D3 国内云的三条友好性要求

1. **免代理直连**:provider 带 `region` 字段;`region: cn` 的服务在国内服务器上直连,平台绝不给它们默认挂代理。反过来,`region: global` 的 provider 支持 per-provider 代理配置(注入到会话 env 的 `HTTPS_PROXY`),两类互不污染。
2. **阿里云、腾讯云开箱即用**:预置表首发即含阿里云百炼(DashScope OpenAI 兼容端点 + 面向 Claude Code 的 Anthropic 兼容代理)与腾讯混元(OpenAI 兼容端点),字段留好、文档链接留好,用户只填 API Key。
3. **凭据按会话注入**(继承 ADR-0001):provider 的 API Key 存宿主侧(按用户/按项目),派会话时才注入该会话的 tmux 环境——同一个 Cell 里,A 会话跑百炼、B 会话跑混元、C 会话跑 Anthropic 互不可见。

### D4 首发预置清单

| provider | region | openai 兼容 | anthropic 兼容 | 认证 env |
|---|---|---|---|---|
| anthropic | global | — | api.anthropic.com | `ANTHROPIC_API_KEY` / OAuth |
| openai | global | api.openai.com/v1 | — | `OPENAI_API_KEY` |
| **aliyun-bailian** | **cn** | dashscope.aliyuncs.com/compatible-mode/v1 | 有(Claude Code 代理,以官方文档为准) | `DASHSCOPE_API_KEY` |
| **tencent-hunyuan** | **cn** | api.hunyuan.cloud.tencent.com/v1 | — | `HUNYUAN_API_KEY` |
| deepseek | cn | api.deepseek.com | api.deepseek.com/anthropic | `DEEPSEEK_API_KEY` |
| moonshot (Kimi) | cn | api.moonshot.cn/v1 | api.moonshot.cn/anthropic | `MOONSHOT_API_KEY` |
| zhipu (GLM) | cn | open.bigmodel.cn/api/paas/v4 | open.bigmodel.cn/api/anthropic | `ZHIPUAI_API_KEY` |
| openrouter | global | openrouter.ai/api/v1 | — | `OPENROUTER_API_KEY` |

> 端点 URL 会漂移:预置表里的值是"最后已知良好",实现时以各云官方文档核准,且任何字段可被 providers.d 覆盖。

### D5 基础设施云中立(与 D1–D4 相对)

"云适配"只发生在模型服务这一层。**基础设施层(部署宿主)保持云中立、零感知**:AgentCell 对宿主的全部要求是干净 Linux + systemd + podman + cgroup v2,阿里云 ECS、腾讯云 CVM、其他公有云、裸金属一视同仁;核心代码不引任何云厂商 SDK、不绑托管服务与 IAM。允许存在的只是便利层:install.sh 识别国内主流发行版(Alibaba Cloud Linux / OpenCloudOS 等 RHEL 系)、将来的云市场镜像或 Terraform 模板——它们在仓库的 `deploy/` 里,永远不进核心。

同理,K8s 不是被舍弃而是被降级为未来的 Cell 驱动选项:控制面说的是期望状态语言,加 `k8s` 驱动(Cell = StatefulSet)时控制面无需改动。

## 后果

- 新云接入成本 = 一段 yaml;新 runner 接入成本 = 实现五接口点;
- 协议交集校验把"codex 配了个只有 anthropic 协议的 provider"这类错误挡在派工前;
- 影响里程碑:M1 领域建模含 runner/provider/binding 三实体;M6 凭据注入实现 env+文件双路与协议 env 生成;M6 验收加"同 Cell 两会话分别跑阿里云百炼与 Anthropic,env 互不可见"。
