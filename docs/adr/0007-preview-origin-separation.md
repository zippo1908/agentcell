# ADR-0007: 不可信预览内容独立 origin(而非沙箱降级)

- 状态:已接受(2026-08)
- 关联:强化 [ADR-0001](0001-architecture.md) 的信任模型;取代最初"用
  opaque-origin 沙箱约束预览"的做法

## 背景

`/preview/<cell>/` 与 `/app/<cell>/` 提供的是**仓库与 agent 编写的代码**——
不可信——但此前与控制台(UI + 控制 API)**同源**。后果:

- 预览页脚本可读取控制台 DOM;
- 可携带操作者的 cookie 调用 dispatch / review / release;
- SameSite cookie **完全不起作用**(同源即同站),CSRF 的 Origin 校验也救不了
  ——同源请求的 `Origin` 头就等于控制台自己的 origin,校验必然放行。

第一版修复是给 iframe 和响应加 `sandbox`(不含 `allow-same-origin`),把预览
压进 **opaque origin**。安全性达标,但**代价不可接受**:opaque origin 下预览
应用对**它自己**也失去同源身份——`document.cookie` 不可用、
`localStorage`/`sessionStorage`/IndexedDB 抛 SecurityError、Service Worker
注册失败、调自己后端变跨源且不带凭据。**带登录态的真实产品预览会直接坏掉**,
而"边看边校准"正是本平台的核心体验。

## 决策:把不可信内容搬到独立 origin

celld 监听两个端口:

| | 控制台 | 预览 |
|---|---|---|
| 默认地址 | `:8080` | `:8081` |
| 提供 | UI(SPA)+ 控制 API + 登录 | **仅** `/preview/<cell>/`、`/app/<cell>/` |
| 内容可信度 | 我们自己的代码 | 仓库/agent 编写,不可信 |

- 两个 mux **互不包含对方的路由**:预览 origin 上没有任何控制 API 或 SPA;
  控制台 origin 上不再代理任何不可信内容(有测试断言两侧都为真)。
- UI 通过 `/api/meta` 的 `previewOrigin` 拿到**绝对 URL** 构造 iframe;写成
  相对路径会让内容退回控制台 origin,隔离随即崩塌。
- 部署可配 `--preview-origin`(如 `https://preview.example.com`);未配置时由
  请求 host + 预览端口推导(port-forward 开发即可用)。

### 由此得到的两全

- **预览零退化**:预览内容对自己是完整同源,`allow-same-origin` 可以安全授予
  ——cookie、localStorage、Service Worker 全部正常,行为与独立打开一致。
- **控制台受保护**:跨源脚本读不到控制台 DOM;cookie 认证的写请求因 `Origin`
  不匹配被 403(ADR 同批实现的 CSRF 校验);即便 GET 带上 cookie,响应也因无
  CORS 头而不会暴露给预览脚本。
- 仍保留的 CSP `sandbox`(含 `allow-same-origin`)只禁止一件事:**导航或替换
  顶层控制台页面**——这与 origin 无关,任何被框内容都不该做。

## 决策补充:每 (Cell, zone) 独立 host + 单次预览票据

单一预览 origin 仍有三个洞,必须一并堵:

1. **Cell 之间同源**:共用预览 origin 时,Cell A 的恶意预览代码可以
   `fetch('/preview/cell-B/')` 并**读到内容**——违背 ADR-0001 的"跨项目强隔离"。
2. **dev 与 prod 同源**:两个区都在同一 host 上时,agent **未经批阅**的产物可以
   读取已发布的正式构建、共享其 storage,甚至给它注册 Service Worker。
3. **控制台 cookie 会漏到预览 origin**(cookie 不区分端口),而且**反向代理还会
   把它连同 `Authorization` 一起转发给上游**——"不接受"不等于"不转发"。

对应决策:

- **每 (Cell, zone) 一个 host**:配置 `--preview-domain=preview.example.com` 时,
  dev 区是 `<cell>-dev.preview.example.com`,prod 区是 `<cell>-prod.…`。这是
  浏览器层面**唯一**能隔离 Cell 之间、以及 dev 与 prod 的手段,生产必须配置
  (需泛域名解析与泛证书)。
- **单次票据,绑定 cell + zone + host**:控制台签发 **2 分钟、一次性**的 HMAC
  票据(带 nonce,首次使用即消费,从历史记录或日志里捡到也无法重放;密钥由
  访问令牌派生,令牌轮换即全部失效)。预览监听器校验 **cell、zone、host 三者
  全部匹配**后,换发一张 **8 小时的会话票据**写入按 zone 路径限定的 HttpOnly
  cookie——**cookie 寿命与其承载的会话一致**,不会出现"cookie 说 8 小时、里面
  装着 2 分钟票据"导致标签页突然 401。dev 票据打不开 prod。
- **预览 origin 不接受控制台 cookie,也不接受 bearer**。
- **代理出站剥离平台凭据**:`Authorization`、转发型 token 头,以及所有平台保留
  cookie(`agentcell_*`、`casdoor*`、`apisix_*`、`CASTGC`、`JSESSIONID`)在请求
  到达不可信上游之前被移除;**预览应用自己的 cookie 原样保留**。

因此 UI 不再自行拼预览地址:服务端在 `/api/cells` 响应里给出带票据的
`previewURL` / `productionURL` 绝对地址。

## 与网关(Casdoor / APISIX 等)共存

- **控制台 cookie 在 TLS 下使用 `__Host-` 前缀**:浏览器强制它 host-only、
  `Secure`、`Path=/`,因此**同级子域无法"抛"一个同名 cookie 给控制台**
  (cookie tossing)。明文 HTTP(开发用 port-forward)不能用该前缀,自动降级。
- **`X-Forwarded-*` 默认不信任**,需显式 `--trust-forwarded-headers`。若无条件
  信任,任何直连 celld 的人都能声明"我们的 origin 是 evil.example",同源校验
  随即形同虚设。**网关必须覆盖(set)而不是追加(append)这些头**,并确保
  celld 不能被绕过网关直接访问。
- **建议预览与控制台使用不同的可注册主域**(而非同一主域的两个子域)。
  `__Host-` 已经挡住会话 cookie 的 tossing,但不同主域能从根上排除子域之间的
  cookie 交互。

## 后果与残留风险

- **未配置 `--preview-domain` 时,所有 Cell 与两个区共享一个预览 origin**。
  此时保护只剩"按 (cell, zone) 路径限定的票据 cookie":另一区/另一 Cell 的
  路径不匹配因而拿不到 cookie,但这依赖浏览器的 cookie 路径规则,**不是源
  隔离**。**多租户或仓库不可信的部署必须配置 `--preview-domain`**。
- 控制台 cookie 泄漏问题已由"预览 origin 不接受控制台凭据"根除,不再依赖
  端口/主机差异。
- 部署面多一个端口/Service 端口(chart 与 install.yaml 已含)。
- 反代/Ingress 需要把预览主机指到 `preview` 端口——部署文档已给示例。
