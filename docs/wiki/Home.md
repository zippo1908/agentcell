# AgentCell

**给 AI 开发员工一个真正能干活的地方。**
*Somewhere for an AI coding agent to actually work.*

你把一个 git 仓库交给它,它为这个项目开一间常驻的工作间——代码、能打开看的预览、
还有单独放"已发布版本"的地方。你用大白话交代要做什么,可以打开一个真终端看它干、
甚至直接插话。**agent 写的东西不会自己进主干**:每段活落在自己的分支上,等人看过
才变成 PR。

---

## 我该从哪读起

| 你想问的 | 去哪 |
|---|---|
| 这到底是个什么东西? | 就下面这一段 |
| 这堆名词谁是谁? | [概念模型](Concepts) |
| 我只有一台服务器,够吗? | [关于「集群」](Cluster) |
| 怎么让同事用起来? | [装起来](Install) |
| agent 在干嘛,我看得见吗? | [终端与休眠](Terminal-and-Dormancy) |
| 有没有比"填表派工"更顺的? | [黑板](Board) |
| 谁能动什么? | [权限](Permissions) |
| 内网拉不到镜像 | [镜像与内网](Images) |
| 出问题先看哪 | [排障](Troubleshooting) |

## 用起来是什么样

1. **建一个项目** —— 指一个仓库,从列表里挑 agent 和模型。大约一分钟起来。
2. **交代一件事** —— 在黑板上打 `@shop 把商品卡片改成两列`。
3. **想看就看** —— 它接单时回一句,做完再回一句;想知道在干嘛就打开终端。
4. **看它做了什么** —— 产出是一条带 diff 的分支。批准变 PR,发布进正式区。

**这些都不需要你懂 Kubernetes。** 用它是为了让项目之间互相踩不到,以及机器重启
不会把活弄丢。

---

<details>
<summary><b>English</b> — the same page</summary>

You give AgentCell a git repository. It sets up a private workspace for that
project and keeps it running: a checkout, a live preview of the app, and a
separate place for the released version. You tell an agent what you want in
ordinary words, and you can open a real terminal and watch it work — or type
into it, and interrupt.

**Nothing the agent writes reaches your main branch on its own.** Every piece
of work lands on its own branch and waits for a person to read it. Only then
does it become a pull request.

**Where to start**

| You want to know | Page |
|---|---|
| What are all these words? | [Concepts](Concepts) |
| I only have one server — is that enough? | [About "the cluster"](Cluster) |
| How do I set this up for my team? | [Install](Install) |
| Can I see what the agent is doing? | [Terminal and dormancy](Terminal-and-Dormancy) |
| Is there something nicer than a dispatch form? | [The board](Board) |
| Who is allowed to do what? | [Permissions](Permissions) |
| Our cluster can't reach ghcr.io | [Images](Images) |
| Something is broken | [Troubleshooting](Troubleshooting) |

**What using it looks like**

1. **Make a project** — point it at a repo, pick an agent and a model from a
   list. It comes up in about a minute.
2. **Ask for something** — on the team board: `@shop make the product cards
   two columns`.
3. **Watch, or don't** — it answers when it takes the job and when it
   finishes. Open its terminal if you want to see.
4. **Read what it did** — the work arrives as a branch with a diff. Approve
   it and it becomes a pull request.

Nobody needs to know Kubernetes for any of this. It runs on Kubernetes so
projects cannot tread on each other, and so a restart does not lose your work.

</details>
