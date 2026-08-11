# fanout

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

把 VPN Gate 的公共节点变成本地 SOCKS5 端口：一个端口一个出口 IP。
再给每个出口挂一个节点链接，客户端连哪个端口就从哪个国家出去。

节点链接有两种管法：同机装了 3x-ui 就接管面板里的入站，没装则 fanout
自己跑 Xray，建站、改站、发链接都在同一个界面里完成。

![主界面](https://images.joeyblog.net/2026/7/27/fanout-dashboard.png)

四条隧道跑在一台机器上，四个端口对应四个国家的出口，母机自己的 IP 不受影响：

![出口验证](https://images.joeyblog.net/2026/7/26/fanout-6-exit-ip.png)

## 原理

每个节点跑在独立的 network namespace 里，netns 内启动官方 openvpn 客户端。
SOCKS5 监听在母机，出站连接用 `setns` 切进对应 netns 建立。

这样做的好处：VPN 的路由劫持只影响自己的 netns，不会切断母机的网络；
多个节点互不干扰，各自一个出口 IP。

```
客户端 ──> 母机 SOCKS5 :随机端口 ──> netns foN ──> openvpn ──> VPN Gate 节点
```

## 安装

需要 root，Linux（依赖 netns）。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/main/install.sh)
```

会自动下载对应架构的预编译二进制。也可以 clone 仓库后在源码目录运行同一个脚本，
那样会从源码编译（需要 Go 1.21+）。

依赖（openvpn / curl / openssl / iproute / iptables）会按发行版自动装，
apt、dnf、yum、pacman、apk、zypper 都认。没装 3x-ui 时还会顺带下载一份
Xray 到 `/var/lib/fanout/bin/`，装了则跳过，入站交给面板管。

服务用 systemd 或 OpenRC 都能装，装完自动开机自启。

**Alpine** 默认不带 bash，先装一下：

```bash
apk add bash
bash <(curl -fsSL https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/main/install.sh)
```

另外 fanout 要在 netns 里跑 openvpn，**宿主必须放开 `/dev/net/tun`**。
不少 LXC 小鸡没给这个权限，`ls /dev/net/tun` 不存在且 `mknod` 报
Operation not permitted 的话，这台机器用不了，跟发行版无关。

装完敲 `f` 打开管理菜单：

![管理菜单](https://images.joeyblog.net/2026/7/26/fanout-7-menu.png)

装完会打印管理界面地址、访问路径和口令：

```
管理界面  http://<你的IP>:8899/gwPuWHvaNr/
访问口令  f81120ac328d11c11b
```

路径和口令都是随机生成的，分别存在 `/var/lib/fanout/basepath` 和
`/var/lib/fanout/password`。路径不对一律返回 404，扫端口的看不到这里跑着什么。

## 使用

界面以**出口**为单位：一行就是一条隧道加上挂在它上面的节点链接。

点「新建出口」，选地区和数量，再选一个已有节点作模板，提交后 fanout 会并行
拉起隧道、为每个出口复制一份节点链接并绑好，进度按目标逐条回报。原来要手点
五步跨两栏的事，现在一次点击十几秒完成。

![新建出口](https://images.joeyblog.net/2026/7/27/fanout-wizard.png)

每行右侧两个按钮：换一个节点（出口 IP 变、端口不变，已分发的客户端配置不用改），
或者停掉这个出口。

点节点名进详情，可以改端口、备注、启停，管理客户端，以及改绑到别的出口：

![节点详情](https://images.joeyblog.net/2026/7/27/fanout-detail.png)

一个入站可以挂多套客户端凭据，分发给不同的人；每套都能单独重置，
重置后旧链接立即失效。

「导出链接」一次性拿到所有节点链接：

![导出链接](https://images.joeyblog.net/2026/7/27/fanout-export.png)

### 节点链接从哪来

同机装了 3x-ui 就直接接管面板里的入站，面板端口、路径、API token 全自动探测，
开了 SSL 也能识别。没装 3x-ui 时 fanout 自己跑一个 Xray，界面上多一个「新建节点」
按钮，可以选协议（VLESS / VMess / Trojan）、传输（TCP / WebSocket / gRPC /
HTTPUpgrade / XHTTP）和安全层（无 / TLS / REALITY）。

![新建节点](https://images.joeyblog.net/2026/7/27/fanout-newnode.png)

REALITY 的密钥对和 shortId 自动生成；TLS 不填证书就生成自签的，分享链接会带上
证书指纹让客户端固定信任。也可以填自己的证书路径。

两种模式下改端口、启停、加删客户端、绑定出口的操作完全一致，用起来没有区别。
想固定用哪种，加 `-panel 3x-ui` 或 `-panel native` 启动参数。

## 运维

装完后敲 `f` 打开管理菜单：启停、看日志、查隧道、改端口/口令/访问路径、更新、卸载。

```
  状态      运行中
  版本      fanout v0.1.1
  开机自启  enabled

  管理地址  http://1.2.3.4:8899/gwPuWHvaNr/
  访问口令  f81120ac328d11c11b

   1) 启动          2) 停止
   3) 重启          4) 查看日志
   5) 隧道列表      6) 连接信息
   7) 改端口        8) 改口令
   9) 改访问路径   10) 开机自启开关
  11) 更新         12) 卸载
```

也可以直接带参数用：

```bash
f info       # 连接信息
f list       # 隧道列表
f restart    # 重启
f log        # 跟踪日志
f update     # 更新到最新版
f uninstall  # 卸载
```

隧道状态存在 `/var/lib/fanout/state.json`，重启后自动恢复，端口保持不变。

健康检查每 10 秒跑一次，比对出口 IP 是否还是建立隧道时那个——openvpn 挂掉后
netns 仍能经母机 NAT 出网，只看通不通会漏判。连续两次不符就自动换节点重连，
槽位和端口不变，原先指向它的节点链接会自动改绑过去。

## 已知限制

- 只转发 TCP。SOCKS5 收到域名时在本机解析，隧道内不跑 UDP/DNS。
- VPN Gate 是志愿者节点，有相当比例已下线或满员（`AUTH_FAILED`）。
  启动时连不上会自动顺着同地区候选往下试，最多 6 个。
- 管理界面只有随机路径 + 口令登录，没有 HTTPS。放公网建议前面套一层反代。

## 许可

[MIT](LICENSE)。

节点来自 [VPN Gate](https://www.vpngate.net/)（筑波大学的学术实验项目），
本工具只是调用其公开的节点列表并用官方 openvpn 客户端连接，不修改也不代理其服务。
使用时请遵守 VPN Gate 的条款和你所在地的法律。

## 交流

- 交流群：<https://t.me/+ft-zI76oovgwNmRh>
- 视频教程：<https://youtube.com/@joeyblog>
- 博客：<https://joeyblog.net>

用着有问题、或者想要什么功能，去群里说或提 issue。

## Fanout Argo 增强版

本版本在不改变 fanout 原有 VPN Gate / netns / SOCKS5 出口机制的前提下，增加 Cloudflare Tunnel（Argo）入口。

数据路径：

`客户端 → Cloudflare Tunnel → Xray WS 入站 → fanout 指定出口 → VPN Gate → 最终出口 IP`

### 一键安装

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/main/install.sh)
```

安装器会自动准备 Xray 与 cloudflared；正式安装建议使用 GitHub Release 版本。

### Argo 管理

```bash
f argo
```

支持：

- VLESS-WS + Quick Tunnel
- VMess-WS + Quick Tunnel
- VLESS-WS + 固定 Tunnel
- VMess-WS + 固定 Tunnel
- 每条 Argo 绑定一个正在运行的 fanout VPN Gate 出口
- cloudflared 异常退出自动重启
- 节点写入 `/root/info.txt`

### 固定 Tunnel

固定 Tunnel 使用 Cloudflare Remote-managed Tunnel Token。创建 Tunnel 后，在 Cloudflare 的 Public Hostname 中把域名指向 fanout 创建的本机 WS 端口，例如：

`argo.example.com → http://127.0.0.1:23456`

然后在 `f argo` 中选择“固定 Tunnel”，填写域名、Tunnel Token 和 fanout 出口 HostName。

### Quick Tunnel

Quick Tunnel 不需要域名和 Token，cloudflared 会生成 `trycloudflare.com` 域名并自动写入节点。Quick Tunnel 适合测试，不建议作为长期生产节点。
