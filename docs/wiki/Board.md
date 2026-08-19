# 黑板

**要活和交活在同一个地方。**

控制台每个名词都有一个页面,却没有一个可以站的地方:工作台数三个数字,派工表单
埋在项目里的第二个 tab 下面。你可以把整套系统操作一遍,却始终没看见它在发生什么。

黑板是一个项目的一条流:

```
@e2e 把商品卡片改成两列
  → e2e:接了:把商品卡片改成两列        [看这一单 →]
  → e2e:跑完了。终端还开着——去看一眼    [看这一单 →]
```

- **`@工作区`** 或 **`@机器人`** 派工,答复回到同一处
- **`@某人`** 记未读提及
- **读了流就算读过**——一个要人记得点的「标记已读」,最后一定变成一个会说谎的红点

## 输入 @ 会列出可选的人

打一个 `@`,弹出这个项目的成员,外加「机器人」。上下键选、回车或 Tab 确认、
Esc 关掉;菜单开着的时候回车归菜单管,不然选个人就把半句话发出去了。

选中之后插进去的是**邮箱前缀**(`@zhumingze`)——一个人能读、也能自己手打的
写法。两个同事撞前缀时插整个地址(`@li@cn.tinci.com`),因为服务端在有歧义时
拒绝猜:**送错人比没送到更糟。**

> 在此之前,`@` 只认哈希用户 id(`@u-9f3a1c…`)。没有人会打那个,所以实际上
> 从来没有人 @ 到过谁 —— 这个功能不是坏了,是够不着,而**够不着比坏了更难
> 发现**:没有报错,没有红点,只是永远没有人回你。

## 三条设计决定

**帖子存在一个 Board 对象里,不是一帖一个 CR。** 帖子不是任何人要 reconcile 的
资源,一千条小 CR 就是一千个 watch 事件,而它只是给人看的。有上限(300 条)——
Kubernetes 对象不是数据库,**真正的记录是这些工作产出的 git 历史,不是关于它们的聊天。**

**黑板会话独立于个人会话。** 黑板和项目的那段对话属于**这个项目**,不属于恰好开口的
那个人——否则第一个说话的人等于把自己的私有终端借了出去,第二个人则被回答在别人
的会话里。它有自己的 uid、自己的私有目录;项目成员都能驱动它。

**@ 匹配不到时绝不静默。** 单个项目直接路由;多个项目就明说该 @ 哪一个;一个都
没有就说清楚。让人对着一条永远不会来的回复干等,是最糟的失败方式。

这条规则对派工一直是真的,**对 @ 人一直是假的**:名字打错一个字母的帖子和送到了
的帖子,在页面上长得一模一样。现在 @ 不到人也会在流里回一句,说清没找到谁、以及
只能 @ 项目成员。

## 它不替你做的事

- **不替你挑 runner 和模型**:用项目建立时定下的默认搭配;没定就沿用上一次;都
  没有就直说去项目里派一单。硬编一个厂商等于悄悄拿你的预算花在你没选的地方。
- **有多把 key 时不猜**,让你去挑。

---

<details>
<summary><b>English</b> — the board</summary>

The console had a page per noun and nowhere to stand. The board is the project's
message stream: type `@shop make the product cards two columns` and the agent
answers there — once when it takes the job, once when it finishes, both with
a link to the work. `@someone` mentions a person; reading the stream marks it
read, because a "mark as read" button people forget becomes a badge that lies.

Typing `@` opens a picker of the project's members plus the agent. It inserts
the local part of somebody's address — a form a person could also have typed —
and the whole address when two colleagues share one, because the server
refuses to guess between them: reaching the wrong person is worse than
reaching nobody. Before the picker, a mention had to be a hashed user id, so
in practice nobody was ever addressed — a feature that is out of reach is
harder to notice than one that is broken.

Three decisions worth knowing:

- **Posts live in one object, not one per post.** A post is not something
  anybody reconciles, and the durable record of what happened is the git
  history the work produced — not the chat about it. Bounded at 300.
- **The board's conversation with a project is its own session**, not the
  asker's. Otherwise the first person to type lends out their private
  terminal, and the second is answered inside somebody else's session.
- **An `@` that matches nothing never silently does nothing.** One project,
  it routes there; several, it says which to name; none, it says so. This was
  true for dispatch and quietly false for people until the picker landed: a
  post with a mistyped name looked exactly like one that was delivered.

It will not pick a model for you: it uses the project's default pairing, or
the last one used, or it tells you to choose. And with several keys it asks
rather than guessing whose budget to spend.

</details>
