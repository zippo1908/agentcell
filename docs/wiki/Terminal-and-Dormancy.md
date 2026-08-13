# 终端与休眠

## 为什么要有终端

headless 的 agent **在结束前什么都不打印**。所以从外面看,"正在干活"和
"卡死了"完全一样——八分钟空白日志不是跑得慢,是看不见。

会话本来就跑在 tmux 里(为了 CLI 自己的对话管理),终端只是把另一头接到浏览器:
xterm.js 经 WebSocket 接到 **agent 正在敲字的那个窗口**,而且**可写**——能插话、
能 Ctrl-C 打断。

两条边界:

- **只有会话的 owner 能开终端**,项目的 maintainer 也不行。socket 在用户私有目录
  里,它本身就是权限;能看项目 ≠ 能拿别人的键盘。
  - **例外**:黑板那条团队会话,团队成员都能驱动——那段对话是团队的。
- **Origin 必须同源。** 终端是这里最值钱的可劫持目标。

## 休眠:空闲是睡着,不是结束

没有 agent 在跑、也没有人在看,默认 **15 分钟**后转 `Dormant`:

- **交回**:槽位、运行时进程(该用户在这个项目里的会话全睡了,runtime pod 才走)
- **保留**:worktree、CLI 自己的对话,都在卷上

打开终端或追问一句就在原处醒来,**agent 不会重跑**(窗口用 `-restore` 重建)。

没人回来的会话默认 **7 天**后清算——是**发布,不是删除**。一周没看,不等于同意扔掉。

## 两个时钟,别搞混

| 字段 | 含义 | 默认 |
|---|---|---|
| `idleSeconds` | 多久不动就**睡着**(交回算力) | 15 分钟 |
| `ttlSeconds` | 睡着之后多久**清算**(发布产出) | 7 天 |

界面上分别写作「分钟闲置后休眠」和「小时后强制清算」。**它们后果不同:一个
省钱,一个发布。**

## 唤醒可能被挡住

唤醒要重新拿一个槽位。项目满了就得等——**这时终端会把平台给出的真实原因显示
出来**(比如「等待槽位(2 个都在用)」),而不是转圈到超时。

---

<details>
<summary><b>English</b> — terminal and dormancy</summary>

**Why a terminal.** A headless agent prints nothing until it is finished, so
from outside "working" and "stuck" look identical. Your session already runs
in a terminal for the agent tool's own sake; the browser just attaches to the
same one — and you can type into it.

Two boundaries: only the session's owner may attach (the team's board session
excepted, because that conversation is the team's), and the page must be
same-origin — a terminal is the most valuable thing here to hijack.

**Dormancy.** No agent running and nobody watching for ~15 minutes, and the
session sleeps: it gives back its slot and its runtime and keeps the working
copy and the conversation on disk. Opening the terminal or asking one more
thing wakes it where you left off — **the agent is not re-run**. A session
nobody returns to is handed in after 7 days: published, not deleted. A week
of not looking is not consent to throw work away.

**Two clocks, and they are not the same:** `idleSeconds` (default 15 min) is
how long until it sleeps and gives back compute; `ttlSeconds` (default 7 days)
is how long a sleeping session is kept before its work is published. One saves
money, the other publishes.

**Waking can be blocked** — it needs a slot, and a full project has none
spare. The terminal shows the real reason rather than spinning until it times
out.

</details>
