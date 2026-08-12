#!/usr/bin/env bash
# fanout 管理菜单稳定启动器
# 使用已经验证可运行的基线版本，避免补丁脚本再次自修改导致语法损坏。
set -e
BASE_URL="https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/25ea599b477ce852ceddd4d888af2982824fb00e/f.sh"
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
curl -fsSL --max-time 30 "$BASE_URL" -o "$TMP"
# Token 改为明文输入，方便输入过程中核对；只修改这一处，不碰其余逻辑。
sed -i 's/read -rsp "  Tunnel Token: " token/read -rp "  Tunnel Token: " token/' "$TMP"
chmod +x "$TMP"
exec bash "$TMP" "$@"
