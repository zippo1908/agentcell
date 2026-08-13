# 关于「集群」:一台机器也是一个真实部署

**如果你只有一台服务器,这里没有任何东西是浪费的。**

单节点 k3s 提供的是:每个项目一个命名空间、配额与 NetworkPolicy、声明式对账
(挂了自己修)、卷与密钥、镜像拉取。**AgentCell 用到的绝大部分就是这些,和机器
多少无关。** 在这里,Kubernetes 主要是"进程和隔离的管理器",不是"多机调度器"。

## 单机时,哪些能力是空转的

诚实地说清楚,免得你以为有:

| 能力 | 单机现状 |
|---|---|
| **PlacementClass(机器池)** | 只有一个节点,选来选去都是它。**控制台在只有 ≤1 个池时直接不显示这一栏** |
| **celld 选主** | 能防"两个副本互相打架",**防不了这台机器挂**——副本都在同一台上 |
| **一个 Cell 不能跨节点** | 单机时感觉不到;多机之后才会咬人 |
| **任何形式的高可用** | **不存在**。这台挂了,全挂 |

## 加第二台机器

在新机器上:

```sh
# 在现有 server 上取 token
sudo cat /var/lib/rancher/k3s/server/node-token

# 在新机器上
curl -sfL https://get.k3s.io | K3S_URL=https://<server-ip>:6443 \
  K3S_TOKEN=<token> sh -
```

进来之后打上标签,再让管理员定义机器池:

```sh
kubectl label node <new-node> agentcell.io/pool=bigmem

kubectl apply -f - <<'YAML'
apiVersion: agentcell.io/v1alpha1
kind: PlacementClass
metadata: {name: bigmem}
spec:
  displayName: 大内存
  description: 128G,给要跑测试库和构建的项目
  nodeSelector: {agentcell.io/pool: bigmem}
YAML
```

第二个池一出现,新建项目页上的「跑在哪类机器上」**自己就会出现**。

## 什么不会因为加机器而变好

**一个项目仍然跑在一台机器上。** 工作区卷是 ReadWriteOnce,所有 pod 跟着
anchor 走。所以:**按最忙的那个项目选机器规格;要更多容量就加项目,不是加槽位。**

## 跨集群 / 上云

**没有实现**,而且不是加个页面能解决的:要另一套 kubeconfig、另一套镜像仓库、
另一套入口和证书、跨集群的状态回收。这是数量级更大的工程。
