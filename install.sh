#!/usr/bin/env bash
# fanout 安装脚本：装二进制、装服务（systemd 或 OpenRC）、开机自启。
#
# 设计说明：安装阶段不依赖 github.com:443。
# 预编译包由 GitHub Actions 发布到 dist 分支，并通过 raw.githubusercontent.com 提供。
# 这样中国大陆/受限网络的 VPS 只需能访问 raw.githubusercontent.com 即可安装。

set -euo pipefail

WEB_PORT="${WEB_PORT:-8899}"
WORK_DIR="${WORK_DIR:-/var/lib/fanout}"
BIN=/usr/local/bin/fanout
REPO="${REPO:-robertwilliamsc998-ui/fanout-argo}"
ARTIFACT_BRANCH="${ARTIFACT_BRANCH:-dist}"

if [[ $EUID -ne 0 ]]; then
  echo "需要 root 权限（要创建 netns 和改 iptables）" >&2
  exit 1
fi

INIT_SYS=""
if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
  INIT_SYS=systemd
elif command -v rc-service >/dev/null 2>&1; then
  INIT_SYS=openrc
else
  echo "不认识的 init 系统（需要 systemd 或 OpenRC）" >&2
  exit 1
fi

svc_install() {
  if [[ "$INIT_SYS" == systemd ]]; then
    sed "s#-web 8899#-web ${WEB_PORT}#; s#-dir /var/lib/fanout#-dir ${WORK_DIR}#" fanout.service \
      > /etc/systemd/system/fanout.service
    systemctl daemon-reload
  else
    cat > /etc/init.d/fanout <<INITEOF
#!/sbin/openrc-run
name="fanout"
description="fanout - VPN Gate 出口扇出网关"
command="${BIN}"
command_args="-web ${WEB_PORT} -dir ${WORK_DIR}"
command_background=true
pidfile="/run/fanout.pid"
output_log="/var/log/fanout.log"
error_log="/var/log/fanout.log"
respawn_delay=5
respawn_max=0
supervisor=supervise-daemon
depend() { need net; after firewall; }
INITEOF
    chmod +x /etc/init.d/fanout
  fi
}

svc_enable_start() {
  if [[ "$INIT_SYS" == systemd ]]; then
    systemctl enable --now fanout
  else
    rc-update add fanout default >/dev/null 2>&1 || true
    rc-service fanout restart
  fi
}

svc_is_active() {
  if [[ "$INIT_SYS" == systemd ]]; then
    systemctl is-active --quiet fanout
  else
    rc-service fanout status >/dev/null 2>&1
  fi
}

svc_logs_hint() {
  [[ "$INIT_SYS" == systemd ]] && echo "journalctl -u fanout -n 30" || echo "cat /var/log/fanout.log"
}

pkg_for() {
  local cmd="$1" mgr="$2"
  case "$cmd" in
    openvpn) echo openvpn ;;
    curl) echo curl ;;
    openssl) echo openssl ;;
    tar) echo tar ;;
    ip) case "$mgr" in apk|pacman) echo iproute2 ;; *) echo iproute ;; esac ;;
    iptables) echo iptables ;;
    unzip) echo unzip ;;
  esac
}

detect_mgr() {
  for m in apt-get dnf yum pacman apk zypper; do
    command -v "$m" >/dev/null && { echo "$m"; return; }
  done
  echo ""
}

install_pkgs() {
  local mgr="$1"; shift
  case "$mgr" in
    apt-get) apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
    dnf) dnf install -y -q "$@" ;;
    yum) yum install -y -q "$@" ;;
    pacman) pacman -Sy --noconfirm --needed "$@" ;;
    apk) apk add --no-cache "$@" ;;
    zypper) zypper --non-interactive install -y "$@" ;;
  esac
}

MGR=$(detect_mgr)
[[ "$MGR" == "apt-get" ]] && iproute_pkg=iproute2 || iproute_pkg=iproute

need_cmd=()
for c in openvpn curl openssl tar iptables; do
  command -v "$c" >/dev/null || need_cmd+=("$c")
done
command -v ip >/dev/null || need_cmd+=(ip)

if [[ ${#need_cmd[@]} -gt 0 ]]; then
  echo "      缺少: ${need_cmd[*]}"
  [[ -n "$MGR" ]] || { echo "不认识的包管理器，请手动安装后重试" >&2; exit 1; }
  pkgs=()
  for c in "${need_cmd[@]}"; do
    [[ "$c" == ip ]] && pkgs+=("$iproute_pkg") || pkgs+=("$(pkg_for "$c" "$MGR")")
  done
  echo "      安装: ${pkgs[*]}"
  install_pkgs "$MGR" "${pkgs[@]}" || { echo "自动安装失败，请手动安装: ${pkgs[*]}" >&2; exit 1; }
fi

echo "[2/6] 获取程序"
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "不支持的架构: $ARCH" >&2; exit 1 ;;
esac

TMP=""
if [[ -f main.go ]] && command -v go >/dev/null; then
  echo "      从源码编译"
  go build -trimpath -ldflags "-s -w" -o "$BIN" .
  [[ -f fanout.service ]] || { echo "缺少 fanout.service" >&2; exit 1; }
else
  echo "      从 raw.githubusercontent.com 获取预编译包 (${GOARCH})"
  TMP=$(mktemp -d)
  URL="https://raw.githubusercontent.com/${REPO}/${ARTIFACT_BRANCH}/fanout-linux-${GOARCH}.tar.gz"
  if ! curl -fL --connect-timeout 15 --retry 2 --retry-delay 1 "$URL" -o "$TMP/f.tar.gz"; then
    echo "      下载失败: $URL" >&2
    echo "      请检查 raw.githubusercontent.com 是否可访问" >&2
    rm -rf "$TMP"
    exit 1
  fi
  tar xzf "$TMP/f.tar.gz" -C "$TMP"
  [[ -f "$TMP/fanout" ]] || { echo "预编译包损坏：缺少 fanout" >&2; rm -rf "$TMP"; exit 1; }
  install -m 755 "$TMP/fanout" "$BIN"
  [[ -f "$TMP/fanout.service" ]] && cp "$TMP/fanout.service" . || true
  [[ -f "$TMP/f.sh" ]] && install -m 755 "$TMP/f.sh" /usr/local/bin/f || true
fi

echo "[3/6] 准备 Xray"
mkdir -p "${WORK_DIR}/bin"
if command -v /usr/local/x-ui/x-ui >/dev/null 2>&1 || [[ -x /usr/bin/x-ui ]]; then
  echo "      检测到 3x-ui，入站交给面板管，跳过"
elif [[ -x "${WORK_DIR}/bin/xray" ]]; then
  echo "      已有 $("${WORK_DIR}/bin/xray" version 2>/dev/null | head -1)"
elif [[ -n "$TMP" && -x "$TMP/xray" ]]; then
  install -m 755 "$TMP/xray" "${WORK_DIR}/bin/xray"
  echo "      使用安装包内置 Xray"
else
  echo "      当前安装包未携带 Xray；请使用带依赖的最新 dist 包" >&2
  echo "      不再从 github.com 下载 Xray，以避免受限 VPS 再次超时" >&2
fi

echo "[4/7] 准备 Cloudflare Tunnel"
if command -v cloudflared >/dev/null 2>&1; then
  echo "      已有 $(cloudflared --version 2>/dev/null | head -1)"
elif [[ -x "${WORK_DIR}/bin/cloudflared" ]]; then
  echo "      已有 ${WORK_DIR}/bin/cloudflared"
elif [[ -n "$TMP" && -x "$TMP/cloudflared" ]]; then
  install -m 755 "$TMP/cloudflared" "${WORK_DIR}/bin/cloudflared"
  echo "      使用安装包内置 cloudflared"
else
  echo "      当前安装包未携带 cloudflared；请使用带依赖的最新 dist 包" >&2
fi
rm -rf "$TMP" 2>/dev/null || true
TMP=""

echo "[5/7] 放行转发"
sysctl -qw net.ipv4.ip_forward=1
grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf 2>/dev/null || echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.conf
if ! iptables -C FORWARD -s 10.99.0.0/16 -j ACCEPT 2>/dev/null; then iptables -I FORWARD 1 -s 10.99.0.0/16 -j ACCEPT; fi
if ! iptables -C FORWARD -d 10.99.0.0/16 -j ACCEPT 2>/dev/null; then iptables -I FORWARD 1 -d 10.99.0.0/16 -j ACCEPT; fi
command -v netfilter-persistent >/dev/null && netfilter-persistent save >/dev/null 2>&1 || true

echo "[6/7] 安装服务"
if [[ -f f.sh ]]; then
  install -m 755 f.sh /usr/local/bin/f
elif [[ ! -x /usr/local/bin/f ]]; then
  curl -fL --connect-timeout 15 --retry 2 "https://raw.githubusercontent.com/${REPO}/main/f.sh" -o /usr/local/bin/f
  chmod 755 /usr/local/bin/f
fi
mkdir -p "$WORK_DIR"
chmod 700 "$WORK_DIR"
[[ -f fanout.service ]] || { echo "缺少 fanout.service" >&2; exit 1; }
svc_install
svc_enable_start

echo "[6/6] 就绪"
sleep 3
svc_is_active && echo "      服务运行中（${INIT_SYS}）" || { echo "      服务启动失败，看 $(svc_logs_hint)" >&2; exit 1; }

for _ in $(seq 1 10); do
  [[ -s "${WORK_DIR}/password" && -s "${WORK_DIR}/basepath" ]] && break
  sleep 1
done

IP=$(curl -s --max-time 8 http://api.ipify.org || echo "<本机IP>")
BP=$(cat "${WORK_DIR}/basepath" 2>/dev/null || true)
echo
echo "  管理界面  http://${IP}:${WEB_PORT}/${BP}/"
echo "  访问口令  $(cat "${WORK_DIR}/password" 2>/dev/null || echo "见 ${WORK_DIR}/password")"
echo
echo "  路径和口令都是随机生成的，也可以随时查看："
echo "    cat ${WORK_DIR}/basepath"
echo "    cat ${WORK_DIR}/password"
echo
echo "  输入 f 打开管理菜单"
echo
echo "  项目    https://github.com/robertwilliamsc998-ui/fanout-argo"
echo