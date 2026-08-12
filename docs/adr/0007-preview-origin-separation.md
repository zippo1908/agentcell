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

## 后果与残留风险

- **cookie 不区分端口**:同一主机的 `:8080` 与 `:8081` 共享 cookie,浏览器仍
  会把控制台 cookie 发给预览 origin。写操作被 Origin 校验挡住、读响应被 CORS
  挡住,但**生产部署应使用不同主机名**(如 `preview.example.com`)以获得真正
  的 cookie 隔离;`--preview-origin` 就是为此准备的。文档已写明。
- 部署面多一个端口/Service 端口(chart 与 install.yaml 已含)。
- 反代/Ingress 需要把预览主机指到 `preview` 端口——部署文档已给示例。
