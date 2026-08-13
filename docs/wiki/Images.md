# 镜像与内网

## 如果集群拉不到 ghcr.io

**先做这件事,再做别的。** 否则每一次镜像变更都会变成"有人下个 2GB 的包再拷到
节点上"——那不是部署流程,是权宜之计。

```sh
kubectl apply -f deploy/registry/registry.yaml     # registry:2,NodePort 30500
```

然后告诉 containerd 可以用明文 HTTP 跟它说话——**每个节点做一次**,因为没有 TLS
的 registry 默认是被拒绝的。注意用单行 `printf`,别用 heredoc:粘贴时行首带空格
会让结束符失效,`tee` 收不了尾。

```sh
printf 'mirrors:\n  "NODE_IP:30500":\n    endpoint:\n      - "http://NODE_IP:30500"\n' \
  > /etc/rancher/k3s/registries.yaml
systemctl restart k3s
```

从能上网的机器经 kube-api 隧道推镜像,不用开 ingress、不用动防火墙:

```sh
kubectl -n agentcell-system port-forward svc/registry 5000:5000 &
podman push --tls-verify=false 127.0.0.1:5000/devbox-slim:<tag>
```

`podman push` 可断点续传,隧道断了只赔剩下的层。

## 每次构建都给新 tag

pod 是 `IfNotPresent`。**重建一个同名 tag,已经拉过的节点会一直跑旧镜像,而且
任何地方都不报错。** 覆盖 tag 是最常见的"改了没生效"来源。

## 两个 devbox

| | 大小 | 什么时候用 |
|---|---|---|
| `devbox-slim` | 814MB | 绝大多数项目。alpine,带 Claude Code、Codex、git、tmux、httpd |
| `devbox` | 2GB | 项目要装系统包时 |

镜像里必须有项目**预览命令用到的东西**。预览命令是按项目原来的镜像写的,换一个
更瘦的镜像而不看这一点,预览就会只留一句 `executable file not found`。
