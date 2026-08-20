# ADR-0016: Principal 是实体,登录只是它的标识符

Status: accepted
Amends: [ADR-0008](0008-user-identity-and-ownership.md)(它把 `ID()` 定义成 `hash(subject)`)

## Context

一个人的身份此前是**从他的登录方式推导出来的**:

    ID() = "u-" + hex(sha256(subject)[:8])

而这个值被写进了四个地方——Cell 成员表的 `members[].userID`、Secret 上的
`agentcell.io/owner` 标签、`Session.spec.ownerUserID`、以及 `useruid` 分配的
Unix uid(也就是 `/workspace/users/<uid>/` 那棵私有树)。**这四处没法放在一个
事务里改。**

于是「他怎么登录的」一次性地、永久地决定了「平台认为他是谁」。两个后果:

**一、接企业 IdP 会让所有人变成陌生人。**

    密码   subject=user:zhumingze@us.tinci.com  ->  u-dd7f41d41e4f5437
    OIDC   subject=oidc:958ce9f3:zhumingze      ->  u-b5a8e3499eb2168f

不是报错——系统会正确地认为这是一个从没见过的新人,于是他的项目不是他的、
凭据不是他的、工作树打不开。

**二、这个系统里没法给人改邮箱,而且这个代价已经在付了。**

`principalOf` 每次登录都用邮箱重算身份,`users.id` 写进去之后从没被当身份读过。
所以改邮箱 = 换人。**这就是为什么没有改邮箱的接口**:设计把一个正常操作
(结婚改名、公司换域名、部门调动)堵死了,而没有任何地方写着这件事。

## Decision

**把关系反过来。** principal id 分配一次,永不从任何东西推导:

```
principals            id (永久、不透明)
identity_bindings     (provider, subject) -> principal_id
```

一个人可以有多个 binding——Casdoor 一个、Entra 一个、本地密码一个——**加或删
binding 都不触碰 id**,所以那四份denormalized 副本一个都不用动。

用本体的话说:**Principal 是实体,OIDC identity 只是它的一种 Identifier。**

### 让这件事现在做起来是安全的,是「收养」而不是「换发」

迁移**不给任何人换 ID**。每个现有账号当前的 id——已经是
`hash("user:" + email)`,已经写进了 Cell 成员表、Secret 标签、Session 属主——
被**收养**成那个 principal 的**已分配 id**,本地登录成为它的第一条 binding。

**一个值都不移动;变的只是这个值从哪来。** 它从「派生」变成「存储」,同一个数字,
不同的出处。从这一刻起,给一个人加第二种登录方式是免费的。

回填只有两条 `INSERT OR IGNORE`,**没有 UPDATE 也没有 DELETE**,所以它在结构上
不可能损坏既有数据:最坏情况是什么也没插入。

### 未绑定的登录**不**按邮箱认亲

第一次见到的登录属于一个**新** principal。刻意不按邮箱匹配已有账号:**IdP 管理员
可以改任何人的 email claim**,仅凭邮箱相同就合并两个身份,等于让控制 IdP 的人
通过改一个字段接管这里的任何账号。

把一个额外的登录关联到已有 principal 是一次**单独的、有意的行为**,而且需要先用
密码确认一次(见 [AUTHENTICATION.md](../AUTHENTICATION.md))。

### 新 id 和被收养的 id 长得一样

`u-` 加 16 个十六进制字符,和现在写进 K8s 对象里的完全同形。理由是这个值是标签值、
是 CR 字段、还是 Unix uid 的种子——引入第二种形状意味着每一个消费方都要同时接受
两种。**新 id 和旧 id 无法区分,这正是目的:id 不携带任何信息。**

(用 ULID 也能满足「不透明、永久、非派生」这三条属性;选同形是为了不给下游增加
一个必须处理的分支。要改成 ULID,只需要换 `NewPrincipalID` 一个函数。)

## Consequences

**`ID()` 现在优先返回已分配的值**,推导是回退路径——留给没有账号库的部署和静态
令牌。对每一个已有账号,两个值**相同**,因为分配的那个就是从派生的那个收养来的。
所以这次上线对所有人都是 no-op。

**认证路径多一次索引查询。** `fromCookie` 本来就要读账号行(验签需要密码哈希),
所以这是同一条路径上多一次主键命中,不是多一次往返。刻意**不缓存**:binding 在
有人关联或解绑登录时会变,而提供一个过期的身份正是这次改动要消灭的那类 bug。

**改邮箱从此是可以做的**——只要 binding 跟着改,id 不动。接口还没建,但障碍已经
拆掉了。

**还没做:自助关联流程。** 现在能创建 binding 的只有「第一次登录」这一条路,所以
一个已有用户用 SSO 登录会得到一个新 principal(按上面的反接管规则,这是正确的),
而他没有办法把两者关联起来。**这意味着开启 OIDC 之前必须先做这个流程**,否则每个
人都会以新身份进来。这是 P2(Casdoor)的第一项,不是可选项。
