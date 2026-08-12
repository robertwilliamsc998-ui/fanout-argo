#!/usr/bin/env bash
# fanout 管理菜单稳定启动器
# 基于已验证的 25ea599b 基线，只对 Argo 创建/节点输出做安全补丁。
set -e
BASE_URL="https://raw.githubusercontent.com/robertwilliamsc998-ui/fanout-argo/25ea599b477ce852ceddd4d888af2982824fb00e/f.sh"
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
curl -fsSL --max-time 30 "$BASE_URL" -o "$TMP"

python3 - "$TMP" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")

# 固定 Tunnel 的 Argo Token 使用明文输入，便于复制/核对。
s = s.replace('read -rsp "  Tunnel Token: " token', 'read -rp "  Tunnel Token: " token')

start = s.find('argo_menu() {')
end = s.find('\nmenu() {', start)
if start < 0 or end < 0:
    raise SystemExit('找不到 argo_menu() 函数')

new = r'''argo_menu() {
  need_root
  local ck json choice protocol mode host token exit id action port bp resp yes node_port inbound_id actual_port status links
  while true; do
    clear; echo -e "${B}  fanout Argo${N}  ${D}Cloudflare Tunnel → fanout 出口${N}"; echo
    ck=$(argo_api_login); port=$(web_port); bp=$(cat "$WORK_DIR/basepath" 2>/dev/null || true)
    json=$(curl -s --max-time 8 -b "$ck" "http://127.0.0.1:${port}/${bp}/api/argo" 2>/dev/null || echo '[]'); rm -f "$ck"
    if command -v python3 >/dev/null 2>&1; then
      echo "$json" | python3 -c 'import json,sys; a=json.load(sys.stdin); print("  %-4s %-7s %-7s %-28s %-18s %s"%( "ID","协议","模式","域名","出口","状态")); [print("  %-4s %-7s %-7s %-28s %-18s %s"%(x.get("id",""),x.get("protocol",""),x.get("mode",""),x.get("hostname","") or "(等待)",x.get("exit_host",""),x.get("status",""))) for x in a]' 2>/dev/null || echo '  无法解析 Argo 列表'
    else
      echo "$json"
    fi
    echo; echo "  1) 新建 VLESS Argo"; echo "  2) 新建 VMess Argo"; echo "  3) 启动 Argo"; echo "  4) 停止 Argo"; echo "  5) 删除 Argo"; echo "  6) 查看节点"; echo "  0) 返回"
    read -rp "  选择: " choice
    case "$choice" in
      1|2)
        protocol=vless; [[ "$choice" == 2 ]] && protocol=vmess
        echo; echo "  选择模式: 1) 固定 Tunnel  2) Quick Tunnel"; read -rp "  模式: " mode
        [[ "$mode" == 1 ]] && mode=fixed || mode=quick

        if ! choose_argo_exit; then pause; continue; fi
        exit="$ARGO_SELECTED_EXIT"
        echo -e "\n  已选择出口: ${G}${exit}${N}"

        host=""; token=""; node_port=""
        if [[ "$mode" == fixed ]]; then
          read -rp "  Argo 域名: " host
          echo
          read -rp "  Tunnel Token: " token
          echo
          while true; do
            read -rp "  Argo 本地节点端口: " node_port
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
        [[ -n "$node_port" ]] && echo -e "  本地节点端口  : ${node_port}"
        echo -e "  TLS 传输安全  : TLS"
        echo -e "  优选地址      : www.wto.org:443"
        echo -e "  Host/SNI      : ${host}"
        echo -e "  状态          : ${status:--}"
        echo
        echo -e "  ${B}节点连接（优选地址：www.wto.org:443）：${N}"

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
          python3 - "$1" "$host" "$protocol" <<'PY2'
import base64,json,sys
from urllib.parse import urlsplit,parse_qsl,unquote,urlencode

link=sys.argv[1].strip()
argo_host=sys.argv[2].strip()
protocol=sys.argv[3].strip().lower()
preferred='www.wto.org'


def vmess_from_uri(link):
    u=urlsplit(link)
    if not u.username:
        raise ValueError('VMess URI 缺少 UUID')
    uuid=unquote(u.username)
    q=dict(parse_qsl(u.query,keep_blank_values=True))
    path=q.get('path') or unquote(u.path) or '/argo'
    obj={
        'v':'2','ps':q.get('remark') or q.get('name') or 'VMess-Argo',
        'add':preferred,'port':'443','id':uuid,'aid':q.get('aid','0'),
        'scy':q.get('scy','auto'),'net':'ws','type':'none','host':argo_host,
        'path':path,'tls':'tls','sni':argo_host,
    }
    raw=json.dumps(obj,separators=(',',':'),ensure_ascii=False).encode('utf-8')
    return 'vmess://'+base64.b64encode(raw).decode('ascii')


def vmess_from_base64(link):
    raw=link.split('://',1)[1].strip(); raw=unquote(raw); raw += '='*((-len(raw))%4)
    decoded=base64.b64decode(raw.encode('ascii'),altchars=b'-_',validate=False)
    obj=json.loads(decoded.decode('utf-8-sig'))
    obj.update({'add':preferred,'port':'443','net':'ws','type':'none','tls':'tls','host':argo_host,'sni':argo_host})
    obj['path']=obj.get('path') or '/argo'; obj['ps']=obj.get('ps') or 'VMess-Argo'
    enc=base64.b64encode(json.dumps(obj,separators=(',',':'),ensure_ascii=False).encode('utf-8')).decode('ascii')
    return 'vmess://'+enc

try:
    if link.lower().startswith('vmess://'):
        body=link.split('://',1)[1]
        print(vmess_from_uri(link) if '@' in body else vmess_from_base64(link))
    elif link.lower().startswith('vless://'):
        u=urlsplit(link); q=dict(parse_qsl(u.query,keep_blank_values=True))
        q['type']='ws'; q['security']='tls'; q['encryption']='none'; q['host']=argo_host; q['sni']=argo_host
        q['path']=q.get('path') or unquote(u.path) or '/argo'
        print('vless://'+(u.username or '')+'@'+preferred+':443?'+urlencode(q)+'#'+(u.fragment or 'VLESS-Argo'))
    else:
        print(link)
except Exception as e:
    print('NODE_BUILD_ERROR:'+str(e),file=sys.stderr); print(link)
PY2
        }

        if [[ -n "$links" ]]; then
          while IFS= read -r link; do
            [[ -z "$link" ]] && continue
            normalize_link "$link"
          done <<< "$links"
        else
          echo -e "  ${R}未能取得节点连接，请检查 Argo 节点状态。${N}"
        fi
        echo
        pause;;
      3|4)
        read -rp "  Argo ID: " id; action=start; [[ "$choice" == 4 ]] && action=stop
        ck=$(argo_api_login); resp=$(curl -s --max-time 15 -b "$ck" -X PUT "http://127.0.0.1:${port}/${bp}/api/argo?id=${id}&action=${action}"); rm -f "$ck"; echo "$resp"; pause;;
      5)
        read -rp "  Argo ID: " id; read -rp "  确认删除？[y/N]: " yes; [[ ${yes,,} == y ]] || continue
        ck=$(argo_api_login); resp=$(curl -s --max-time 15 -b "$ck" -X DELETE "http://127.0.0.1:${port}/${bp}/api/argo?id=${id}"); rm -f "$ck"; echo "$resp"; pause;;
      6)
        echo; echo "$json" | grep -o '"link":"[^"]*"' | sed 's/^"link":"//;s/"$//' | sed 's#\\u0026#\&#g'; pause;;
      0) return;;
    esac
  done
}
'''

s = s[:start] + new + s[end:]
p.write_text(s, encoding='utf-8')
PY

chmod +x "$TMP"
exec bash "$TMP" "$@"
