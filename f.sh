#!/usr/bin/env bash
# fanout 管理菜单兼容层：修复 Argo 节点输出，尤其是 VMess。
set -euo pipefail

BASE_URL="https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/25ea599b477ce852ceddd4d888af2982824fb00e/f.sh"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
curl -fsSL --max-time 30 "$BASE_URL" -o "$TMP"

python3 - "$TMP" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1])
s=p.read_text()
start=s.index('        host=""; token=""')
end=s.index('      3|4)', start)
new=r'''        host=""; token=""; node_port=""
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
        argo_id=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id", ""))' 2>/dev/null || true)
        inbound_id=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("inbound_id", ""))' 2>/dev/null || true)
        actual_port=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("local_port", ""))' 2>/dev/null || true)
        status=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status", ""))' 2>/dev/null || true)

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
          svc_restart >/dev/null 2>&1 || true
          sleep 2
          status="up"
        fi

        echo
        echo -e "  ${G}Argo 节点创建完成${N}"
        echo -e "  出口          : ${exit}"
        echo -e "  协议          : ${protocol}"
        echo -e "  模式          : ${mode}"
        [[ -n "$host" ]] && echo -e "  Argo 域名     : ${host}"
        echo -e "  TLS 传输安全  : TLS"
        echo -e "  外部地址      : www.wto.org:443"
        echo -e "  优选地址      : www.wto.org"
        echo -e "  Host/SNI      : ${host}"
        [[ -n "$node_port" ]] && echo -e "  本地入站端口  : ${node_port}"
        echo -e "  状态          : ${status:--}"
        echo
        echo -e "  ${B}节点连接（直接复制到客户端）：${N}"

        links=""
        if [[ -n "$inbound_id" ]]; then
          ck=$(argo_api_login)
          detail=$(curl -s --max-time 20 -b "$ck" \
            "http://127.0.0.1:${port}/${bp}/api/xui/detail?id=${inbound_id}" 2>/dev/null || true)
          rm -f "$ck"
          links=$(printf '%s' "$detail" | python3 -c 'import json,sys; d=json.load(sys.stdin); x=d.get("links",[]); print("\n".join(x if isinstance(x,list) else [x]))' 2>/dev/null || true)
        fi
        if [[ -z "$links" ]]; then
          link=$(printf '%s' "$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("link", ""))' 2>/dev/null || true)
          [[ -n "$link" ]] && links="$link"
        fi

        normalize_link() {
          python3 - "$1" "$host" <<'PY2'
import base64,json,sys
from urllib.parse import urlsplit,urlunsplit,parse_qsl,urlencode,unquote
link=sys.argv[1].strip(); argo_host=sys.argv[2].strip(); preferred='www.wto.org'
try:
    if link.lower().startswith('vmess://'):
        raw=link.split('://',1)[1].strip()
        raw=unquote(raw)
        raw += '='*((-len(raw))%4)
        obj=json.loads(base64.urlsafe_b64decode(raw.encode()).decode('utf-8-sig'))
        # Cloudflare Argo 对外固定：优选地址 + 443 + TLS + WS。
        obj['add']=preferred
        obj['port']='443'
        obj['net']='ws'
        obj['type']='none'
        obj['tls']='tls'
        obj['host']=argo_host
        obj['sni']=argo_host
        if not obj.get('path'):
            obj['path']='/argo'
        obj['ps']=obj.get('ps') or 'VMess-Argo'
        enc=base64.urlsafe_b64encode(json.dumps(obj,separators=(',',':'),ensure_ascii=False).encode()).decode().rstrip('=')
        print('vmess://'+enc)
    elif link.lower().startswith('vless://'):
        u=urlsplit(link); q=dict(parse_qsl(u.query,keep_blank_values=True))
        q['type']='ws'; q['security']='tls'; q['encryption']='none'; q['host']=argo_host; q['sni']=argo_host
        path=q.get('path') or u.path or '/argo'; q['path']=path
        user=u.username or ''
        netloc=user+'@'+preferred+':443'
        print(urlunsplit(('vless',netloc,'',urlencode(q),u.fragment or 'VLESS-Argo')))
    else:
        print(link)
except Exception as e:
    print('NODE_BUILD_ERROR:'+str(e),file=sys.stderr)
    print(link)
PY2
        }

        if [[ -n "$links" ]]; then
          while IFS= read -r link; do
            [[ -z "$link" ]] && continue
            normalize_link "$link"
          done <<< "$links"
        else
          echo -e "  ${R}未能从后端取得节点连接，请检查 Argo 节点状态。${N}"
        fi
        echo
        pause;;
'''
p.write_text(s[:start]+new+s[end:])
PY

exec bash "$TMP"
