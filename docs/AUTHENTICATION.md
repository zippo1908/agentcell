# 统一身份认证

**目标**:让 AgentCell 用公司已有的那一套身份,而不是再养一套账号。

这篇讲的是**认证**——你是谁、怎么证明。**授权**(你能做什么)是另一件事,
在 [ADR-0013](adr/0013-authorization.md) 和企业授权控制面的设计里。两者的接缝
只有一个:认证产出一个 **Principal**,授权只认它。

---

## 1. 今天已经有什么

AgentCell 从第一版起就把「你是谁」抽象成了一个类型,而不是散在各处:

```go
type Principal struct {
    Subject string  // 稳定、跨签发方唯一
    Name    string
    Email   string
    Kind    Kind    // oidc | user | token
}
```

三种来源,**都已经实现并在用**:

| Kind | 怎么来的 | 用在哪 |
|---|---|---|
| `oidc` | 标准 OIDC 授权码 + PKCE,celld **自己验签** | 企业 SSO,就是这篇要接的 |
| `user` | 平台自己的账号(邮箱 + argon2id 密码) | 现在 151 上跑的 |
| `token` | 静态 Bearer 令牌 | 破窗用:还没有人的时候把部署装起来 |

OIDC 这条路**不是待办,是已完成的**:`internal/identity/oidc.go` 做发现、验签、
`internal/webui/auth.go` 做授权码流程和 PKCE。集群里 Casdoor 也已经部署
(`deploy/identity/casdoor.yaml`)。**只是当前部署没有配 `--oidc-issuer`,所以走的是账号密码。**

### 两个刻意的决定,接 SSO 时不要推翻

**celld 自己验 ID token,不信任网关塞的身份头。** celld 在集群内是可达的,
信任 `X-Forwarded-User` 之类的头,等于让 pod 网络上任何东西都能声称自己是任何人
——身份层就变成装饰品了(ADR-0008)。网关可以在前面挡一道,但**不能是唯一那道**。

**发现是懒加载的,不在启动时做。** IdP 慢启动时,celld 不会 crash-loop。
一个「因为登录服务没起来所以自己也起不来」的控制面,比一个「暂时不能登录」的更糟。

---

## 2. 唯一真正困难的地方:切换会换掉人的身份

这是这篇文档存在的理由。

Principal 的 `Subject` 决定 `ID()`,而 `ID()` 是**写进 Kubernetes 对象里的那个值**:

```go
ID() = "u-" + hex(sha256(Subject)[:8])
```

同一个人,两种登录方式,算出来是两个人:

```
账号密码  subject=user:zhumingze@us.tinci.com   ->  u-dd7f41d41e4f5437
OIDC     subject=oidc:958ce9f3:zhumingze       ->  u-b5a8e3499eb2168f
```

而这个 ID 决定了**至少四样东西**:

- `Cell.spec.members[].userID` —— 项目成员资格
- Secret 上的 `agentcell.io/owner` 标签 —— 凭据归谁
- `useruid` 分配的 Unix uid —— 也就是 `/workspace/users/<uid>/` 这棵私有树:
  **工作树、CLI 状态、tmux socket、已连接的账号**
- `Session.spec.ownerUserID` —— 谁为这条会话付账,不可变

**所以直接打开 OIDC 开关,每个人都会以一个崭新的陌生人身份登录进来**:项目里没有
他、凭据不是他的、工作树打不开、正在跑的会话不认他。而且**不会有任何报错**——
系统会认为这是一个从没见过的新人,这正是它该有的行为。

> 这不是 bug,是「主体即身份」这个设计的直接后果。它换来的好处是:身份不依赖任何
> 数据库行,存储重建了也不变;而代价就是主体本身必须稳定。

---

## 3. 迁移方案:先建立身份连续性,再切开关

### 第一步:选定并**冻结** subject 的算法

`OIDCSubject(issuer, sub)` 把 issuer 也哈希进去,是为了两个 IdP 恰好用了同一个
`sub` 时不撞车。代价是:**换 issuer URL 就等于换掉所有人的身份。**

所以上线前把这两件事定死,写进部署文档:

- **issuer URL 一个字都不许再改**(包括 http→https、有无末尾斜杠)。
  `--oidc-issuer` 里的值会被 `TrimRight("/")`,但 scheme 和主机名的任何改动都是换人。
- **`sub` 用什么**。Casdoor 可以给 UUID、用户名、邮箱。**选一个此人一辈子不会变的**
  ——邮箱会因为改名、部门调动而变,UUID 不会。选 UUID,把邮箱只当显示用。

### 第二步:身份链接表(必须,不是可选)

在账号库里加一张表,把「旧的 user: 主体」和「新的 oidc: 主体」指向同一个人:

```
identity_links(primary_subject, linked_subject, linked_at, linked_by)
```

解析时:认证产出一个 Principal → 查链接表 → 如果这个 subject 被链接到某个
primary,就用 primary 的 ID。**一个人可以有多个登录方式,只有一个身份。**

这条要放在 `identity` 包里、`ID()` 之前,而**不是**散在各调用点——否则就是又一处
「同一个问题两个函数回答」,而这个仓库已经因为这个吃过三次亏。

### 第三步:自助链接,不是管理员批量改

第一次用 SSO 登录、且这个 subject 没被链接过时:

1. 如果邮箱与某个已有账号相同 → 提示「这看起来是你,用密码确认一次」
2. 密码验证通过 → 写入链接
3. 从此这个人无论用哪种方式登录,都是同一个 ID

**为什么要密码确认**:IdP 上的邮箱声明是可以被管理员改的。仅凭邮箱相同就合并两个
身份,等于让能改 IdP 邮箱的人接管平台上任何一个账号。**一次密码确认把这条路堵死**,
而且只需要做一次。

### 第四步:切换期两种方式并存

账号密码这条路**不要立刻关掉**:

- SSO 出故障时还有路进得来(IdP 是新的单点故障,第一个月一定会有意外)
- 没链接完的人还能登录并完成链接
- 静态令牌**永远保留**:它是把部署装起来的那条路,也是 IdP 彻底不可用时的破窗

链接率到 100% 之后,再把账号密码登录从界面上收起来(**保留后端能力**,只是不 offer)。

---

## 4. 接入清单(实际要做的事)

```bash
celld \
  --oidc-issuer=https://casdoor.tinci.com \
  --oidc-client-id=agentcell \
  --oidc-redirect-url=https://console.tinci.com/oidc/callback
```

IdP 那侧:

- 授权码流程 + PKCE(celld 用的就是这个)
- redirect URI 精确匹配,**不要用通配**
- scopes:`openid profile email`——后两个只影响显示名,不影响身份
- `sub` 稳定不变(见上)

**要一起想清楚的三件**:

**① 预览域是另一个 origin,有它自己的票据。** 不可信内容跑在
`<cell>-<zone>.<domain>` 上(ADR-0007),用一次性票据换 cookie,**从不接受控制台
的会话凭据**。SSO 不能改这件事——把 SSO 会话延伸到预览域,等于把 agent 写的、
没人看过的页面放进你的登录态里。

**② 终端是 WebSocket,走 cookie。** 所以 origin 检查和 CSRF 那道闸不能因为
「已经有 SSO 了」就放松:cookie 会被浏览器自动带上,SSO 不改变这一点。

**③ 机器身份和人的身份是两套。** git-broker 用的是 audience 绑定的
ServiceAccount 令牌(ADR-0005),工作负载 pod **不持有任何能查询控制面的凭据**。
统一认证是**人**的事;不要顺手把机器也塞进 IdP,那会给 pod 一个能反问「我是谁」
的凭据,而那正是这个平台的隔离模型不允许的。

---

## 5. 会话本身

登录之后是一个**签名 cookie**,不是数据库里的一行:

- 签名覆盖身份、过期时间**和密码哈希**——所以改密码会让所有会话立刻失效,
  不需要任何地方记得这些会话存在过
- 7 天滑动窗口:在用就不断续期,闲置一周失效
- HttpOnly、host-only、明文 HTTP 下不带 `Secure`(带了浏览器会静默丢弃)

接 SSO 之后,「改密码即吊销」这条对 OIDC 用户不再成立——**密码不在我们这儿了**。
届时需要的是:IdP 侧登出/禁用 → 平台侧感知。可选做法,按代价从低到高:

1. **缩短 cookie 窗口**(最简单,代价是登录频率)
2. **每人一个撤销 epoch**:签名里带上它,管理员改一次就吊销这个人的全部会话
   ——这也是 celld 多副本要做的三件事之一,可以一起做
3. **后端通道**(OIDC Back-Channel Logout):最完整,也最需要 IdP 配合

---

<details>
<summary><b>English</b> — unified authentication</summary>

**Goal**: AgentCell uses the company's existing identity rather than keeping its own.

The OIDC path is **already implemented** — discovery, verification and the PKCE
authorization-code flow all exist, and Casdoor ships in `deploy/identity/`. The
current deployment simply does not set `--oidc-issuer`, so it uses accounts.

**The one genuinely hard part** is that turning SSO on changes who people are.
`Principal.Subject` determines `ID()`, and that id is what is written into
Kubernetes objects — project membership, credential ownership, the allocated
Unix uid (and therefore the private tree holding worktrees, CLI state and
connected accounts), and a session's immutable payer. The same person logging in
two ways computes to two different ids, and nothing errors: the platform
correctly concludes it has never seen them before.

So the migration is: freeze the issuer URL and the `sub` claim; add an identity
link table consulted inside the `identity` package before `ID()`; let people link
their own accounts with one password confirmation (an email claim alone is not
proof — whoever can edit emails in the IdP could otherwise take over any
account); and keep password login available until every account is linked. The
static token stays forever: it is how a deployment is brought up and how you get
in when the IdP is down.

Three things must not change when SSO lands: the preview origin keeps its own
one-time tickets and never accepts a console session (ADR-0007); the terminal
WebSocket keeps its origin/CSRF checks, because cookies are sent automatically
whatever the login method; and machine identity stays separate from human
identity — workload pods hold no credential that can query the control plane
(ADR-0005), and putting them in the IdP would hand them one.

Finally, "changing your password revokes every session" stops holding once the
password lives in the IdP. Replace it with a per-user revocation epoch carried in
the cookie signature — which is also one of the three things multi-replica celld
needs, so the two are worth doing together.

</details>
