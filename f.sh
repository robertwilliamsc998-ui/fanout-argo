#!/usr/bin/env bash
# Compatibility wrapper: load the last stable interactive manager and apply
# the Argo fixed-Tunnel port/token/output fixes at runtime.
set -euo pipefail

BASE_URL="https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/25ea599b477ce852ceddd4d888af2982824fb00e/f.sh"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

curl -fsSL --max-time 30 "$BASE_URL" -o "$TMP"

python3 - "$TMP" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text()

start = s.index('        host=""; token=""')
end = s.index('      3|4)', start)

new = r'''        host=""; token=""; node_port=""
        if [[ "$mode" == fixed ]]; then
          read -rp "  Argo 域名: " host
          echo
          read -rsp "  Tunnel Token: " token
          echo
          echo -e "  ${D}已输入 Token（完整显示用于核对）：${N}"
          echo "  ${token}"
          echo
          while true; do
            read -rp "  节点本地端口: " node_port
            if [[ "$node_port" =~ ^[0-9]+$ ]] && (( node_port >= 1 && node_port <= 65535 )); then
              if ss -lnt 2>/dev/null | grep -qE ":${node_port}[[:space:]]"; then
                echo -e "  ${R}端口 ${node_port} 已被监听，请换一个。${N}"
                continue
              fi
              break
            fi
            echo -e "  ${R}端口无效，请输入 1-65535。${N}"
          done
        fi

        ck=$(argo_api_login)
        resp=$(curl -s --max-time 30 -b "$ck" -X POST \
          --data-urlencode "protocol=${protocol}" \
          --data-urlencode "mode=${mode}" \
          --data-urlencode "hostname=${host}" \
          --data-urlencode "token=${token}" \
          --data-urlencode "exit=${exit}" \
          "http://127.0.0.1:${port}/${bp}/api/argo")
        rm -f "$ck"

        if [[ -z "$resp" ]]; then
          echo -e "  ${R}创建 Argo 失败：没有收到服务器响应。${N}"
          pause
          continue
        fi

        argo_id=""; inbound_id=""; actual_port=""; status=""
        if command -v python3 >/dev/null 2>&1; then
          argo_id=$(printf '%s' "$resp" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("id", ""))' 2>/dev/null || true)
          inbound_id=$(printf '%s' "$resp" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("inbound_id", ""))' 2>/dev/null || true)
          actual_port=$(printf '%s' "$resp" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("local_port", ""))' 2>/dev/null || true)
          status=$(printf '%s' "$resp" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status", ""))' 2>/dev/null || true)
        fi

        # 当前后端新建入站默认随机端口。固定 Tunnel 的服务端口必须与
        # Cloudflare Tunnel 配置一致，所以创建后立即切换到用户指定端口。
        if [[ "$mode" == fixed && -n "$node_port" && -n "$inbound_id" && "$actual_port" != "$node_port" ]]; then
          uck=$(argo_api_login)
          upd=$(curl -s --max-time 20 -b "$uck" \
            "http://127.0.0.1:${port}/${bp}/api/panel/inbound/update?id=${inbound_id}&port=${node_port}" 2>/dev/null || true)
          rm -f "$uck"
          if ! printf '%s' "$upd" | grep -q '"ok"'; then
            echo -e "  ${R}节点已创建，但指定端口 ${node_port} 设置失败。${N}"
            echo "  更新结果: ${upd}"
            pause
            continue
          fi

          # 同步持久化的 Argo local_port。然后重启 fanout，让内存状态、
          # 节点列表和分享链接全部使用新的端口。
          if [[ -f "$WORK_DIR/argo.json" && -n "$argo_id" ]]; then
            python3 - "$WORK_DIR/argo.json" "$argo_id" "$node_port" <<'PY2' 2>/dev/null || true
import json,sys
p, aid, port = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
try:
    d=json.load(open(p))
    for x in d.get("argo", []):
        if int(x.get("id",0)) == aid:
            x["local_port"] = port
    with open(p,"w") as f:
        json.dump(d,f,ensure_ascii=False,indent=2)
except Exception:
    pass
PY2
          fi
          svc_restart >/dev/null 2>&1 || true
          sleep 2
          status="up"
        fi

        echo
        echo -e "  ${G}Argo 节点创建完成${N}"
        echo -e "  出口      : ${exit}"
        echo -e "  协议      : ${protocol}"
        echo -e "  模式      : ${mode}"
        [[ -n "$host" ]] && echo -e "  Argo 域名 : ${host}"
        [[ -n "$node_port" ]] && echo -e "  节点端口  : ${node_port}"
        echo -e "  状态      : ${status:--}"
        echo
        echo -e "  ${B}节点连接（直接复制到客户端）：${N}"

        links=""
        if [[ -n "$inbound_id" ]]; then
          ck=$(argo_api_login)
          detail=$(curl -s --max-time 20 -b "$ck" \
            "http://127.0.0.1:${port}/${bp}/api/xui/detail?id=${inbound_id}" 2>/dev/null || true)
          rm -f "$ck"
          links=$(printf '%s' "$detail" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("\\n".join(d.get("links",[])))' 2>/dev/null || true)
        fi

        if [[ -n "$links" ]]; then
          while IFS= read -r link; do
            [[ -n "$link" ]] && echo "$link"
          done <<< "$links"
        else
          link=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("link", ""))' 2>/dev/null || true)
          [[ -n "$link" ]] && echo "$link"
        fi
        echo
        pause;;
'''

p.write_text(s[:start] + new + s[end:])
PY

exec bash "$TMP"
