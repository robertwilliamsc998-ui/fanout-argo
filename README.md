# fanout-argo

Fanout + VPN Gate 出口管理 + Xray/3x-ui 节点绑定 + Cloudflare Argo。

## 核心设计

**出口和节点完全分离。**

- 出口可以是 1 个或多个。
- 出口 HostName、地区、出口 IP、SOCKS5 端口均由 VPN Gate 运行时动态返回。
- 程序不假定固定的 1/2/3 个出口，也不写死任何 HostName。
- 节点由用户**手工创建**。
- 节点由用户**手工绑定**到指定出口。
- 一个节点只绑定一个固定出口。
- 一个出口可以绑定 0 个、1 个或多个节点。
- 新增出口不会自动生成节点。
- 不提供按出口复制/克隆节点的功能。
- 节点的 UUID、密码、端口、路径等不会因为新增出口而自动复制或改变。

## 数据流

```text
VPN Gate 动态出口
    ├── 出口 A / 随机 HostName / 随机 SOCKS5
    ├── 出口 B / 随机 HostName / 随机 SOCKS5
    └── 出口 N / 随机 HostName / 随机 SOCKS5

手工节点 1 ──手工绑定──> 出口 A
手工节点 2 ──手工绑定──> 出口 B
手工节点 3 ──手工绑定──> 出口 N
```

例如：

```text
VLESS 节点 22521
    ↓
BoundTo = vpn122916437
    ↓
fanout-vpn122916437
    ↓
SOCKS5 127.0.0.1:31344
    ↓
VPN Gate / Japan
```

`vpn122916437` 只是某一次运行时由 VPN Gate 返回的 HostName，不是固定出口名称。

## 固定出口绑定

节点绑定关系持久化在：

```text
/var/lib/fanout/native.json
```

核心字段：

```json
{
  "id": 2,
  "port": 22521,
  "protocol": "vless",
  "remark": "vless-22521",
  "bound_to": "vpn122916437"
}
```

Xray 配置会根据 `bound_to` 精确生成对应的 SOCKS5 outbound 和 routing rule：

```text
inbound
  ↓
fanout-<出口HostName>
  ↓
127.0.0.1:<该出口随机 SOCKS5 端口>
  ↓
VPN Gate
```

因此不会出现“节点自动平均分配出口”或“所有节点默认共用第一个出口”。

没有绑定出口的节点不会自动挑选一个出口。

## 出口管理

Web 管理界面可以：

1. 拉取 VPN Gate 节点列表。
2. 启动一个或多个出口。
3. 查看每个出口的动态 HostName、地区、IP 和 SOCKS5 端口。
4. 停止出口或重新选择出口。
5. 手工维护节点与出口的绑定关系。

出口数量没有固定的 1/2/3 限制，实际数量受 `-max` 参数和服务器资源限制。

## 节点管理

### Native 模式

fanout 自己运行 Xray。用户手工新建 VLESS / VMess / Trojan 等入站，然后在节点详情中选择一个正在运行的出口绑定。

### 3x-ui 模式

fanout 接管现有 3x-ui 的 Xray 配置，只负责把已有入站路由到用户指定的 VPN Gate 出口。

### 明确不会做的事情

- 不会因为创建出口自动生成一个节点。
- 不会根据一个模板批量复制节点。
- 不会因为新增出口复制现有节点。
- 不会把一个节点同时绑定多个出口。

## Argo

Argo/Cloudflare Tunnel 是入口，VPN Gate 是最终出口，两者是不同层次：

```text
客户端
   ↓
Cloudflare Tunnel / Argo
   ↓
Xray 节点入站
   ↓
节点手工绑定的 VPN Gate 出口
   ↓
最终出口 IP
```

Argo 入站同样可以绑定指定的 fanout 出口，不应把 Argo 名称当成 VPN Gate 出口名称。

## 安装

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/main/install.sh)
```

需要 root，并要求宿主机具备 `/dev/net/tun`、network namespace、iptables 等能力。

## 运行

```bash
f
```

默认 Web 端口：`8899`。

工作目录：

```text
/var/lib/fanout/
```

主要文件：

```text
state.json       # 动态出口状态
native.json      # Native 节点及手工 bound_to 关系
xray.json        # 当前 Xray 运行配置
argo.json        # Argo 配置
settings.json    # Web 设置
```

## 许可证

MIT。
