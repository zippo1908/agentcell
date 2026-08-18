# 装起来

**目标是同事开机即用。** 顺序有讲究:先决定,再执行。

## 先决定

| 决定 | 它卡住什么 |
|---|---|
| **装在哪台机器上,以及允不允许装** | k3s 会占 80/443 并写 iptables。在一台已经在服务的机器上决定这件事,就是事故的开始 |
| **两个域名** | 控制台一个;**不可信的预览内容必须在另一个可注册域**,否则预览里的应用能给控制台种 cookie |
| **谁是身份提供方** | 不配 OIDC 的话,所有人是同一个主体,什么都不是私有的 |
| **模型 key 谁的、几把** | key 按人给、按人花;共用一把就是共用账单和共用责任 |
| **正式区在 Cell 内还是交出去** | 生产已经有自己的流水线,就交出去,别在这儿跑第二份 |

## 装

```sh
helm install agentcell oci://ghcr.io/zippo1908/charts/agentcell \
  --namespace agentcell-system --create-namespace \
  --set celld.auth.tokens="{$(openssl rand -hex 24)}" \
  --set preview.domain=preview.example.com --set preview.ingress.enabled=true
```

**内网拉不到 ghcr.io 就先读 [镜像与内网](Images)。**

## 多副本(可选)

celld 持 lease,**只有一个副本 reconcile,每个副本都服务控制台**。要开多副本还得
给一把共享的预览签名密钥,否则**一个副本签的票另一个不认**,预览会间歇性失败——
chart 会直接拒绝渲染并说明原因。

```sh
kubectl -n agentcell-system create secret generic celld-preview-key \
  --from-literal=previewKey="$(openssl rand -hex 32)"
helm upgrade ... --set celld.replicas=2 --set previewKeySecret=celld-preview-key
```

杀掉 leader 实测约 4 秒接管。**已经在跑的会话不受影响**——它们是自治的 pod。

## 首次上线清单

```
[ ] 机器定了,且允许在上面装
[ ] 控制台域名 + TLS
[ ] 预览域名在另一个可注册域 + 泛域名证书
[ ] 镜像拉得到(内网就先起 registry)
[ ] OIDC + 一个 break-glass token
[ ] git 凭据(绑定到它的仓库)
[ ] 每人一把模型 key,标上属主
[ ] 第一个项目
[ ] 一次真的派工:到得了模型,清算推得上 forge
```

**最后一行才是唯一能证明前面都对的那一行。**

---

<details>
<summary><b>English</b> — setting it up</summary>

**Decide first, then run.** Which machine, and is installing on it agreed
(k3s takes ports 80/443 and writes iptables rules — deciding this on a box
that already serves something is how an outage starts). Two DNS names: one
for the console, and **a different registrable domain** for previews, or a
previewed app can set cookies the console receives. Who your identity
provider is — without one, everyone is the same person and nothing is
private. Whose model keys. And whether production runs here or is handed off
to a pipeline that already owns it.

Then one helm command. If your cluster cannot reach ghcr.io, read Images
first.

**More than one console replica** needs a shared preview signing key as well —
otherwise a ticket minted by one replica is refused by the others and
previews fail intermittently. The chart refuses to render rather than let
that happen. Killing the leader takes about four seconds to hand over;
sessions already running are untouched.

**The last line of the checklist is the only one that proves the rest:** one
real dispatch that reaches the model, and a hand-in that reaches the forge.

</details>
