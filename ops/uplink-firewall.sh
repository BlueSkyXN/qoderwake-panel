#!/bin/bash
# 只下不上 · A3 网络兜底（双档）：
#   add      lite（默认安全档）：HTTPDNS DROP + 仅守"边缘 IP"（daemon 实测不经由的），控制面主 IP 不动
#   add-full 激进档：控制面 IP 全部仅限网关用户直连。
#            ⚠ 实测会掐死会话派发（daemon 存在无视 env 覆盖的 openapi 直连客户端，/api/session-start/ 链路），
#              除非模式 B 补丁收编该客户端，否则不要用 full。
#   remove / status 同常规。BYOK provider/资源 CDN/回环不受影响。
# 控制面 IP 取 $QW_ROOT/config/cp-ips.txt；必需直连 IP 取 $QW_REQUIRED_DIRECT（默认 47.86.184.84 47.86.185.172）。
set -u
IPT() { iptables -w 5 "$@"; }
ACT=${1:-status}
GW_USER=${QW_GW_USER:-qwgw}
GW_UID=$(id -u "$GW_USER" 2>/dev/null) || { echo "需要系统用户 $GW_USER（useradd -r $GW_USER）"; exit 1; }
ROOT=${QW_ROOT:-}
CP_FILE="$ROOT/config/cp-ips.txt"
REQ_DIRECT=${QW_REQUIRED_DIRECT:-"47.86.184.84 47.86.185.172"}

if [ $# -ge 2 ]; then
  CP_IPS=$2
elif [ -f "$CP_FILE" ]; then
  CP_IPS=$(grep -vE '^\s*#|^\s*$' "$CP_FILE" | tr '\n' ' ')
else
  CP_IPS=""
fi

is_required() { for r in $REQ_DIRECT; do [ "$1" = "$r" ] && return 0; done; return 1; }

apply_add() {  # $1=full 时含必需直连 IP，否则跳过
  for ip in $CP_IPS; do
    [ "$1" != full ] && is_required "$ip" && continue
    IPT -C OUTPUT -p tcp -d "$ip" --dport 443 -m owner --uid-owner "$GW_UID" -j ACCEPT 2>/dev/null || \
      IPT -A OUTPUT -p tcp -d "$ip" --dport 443 -m owner --uid-owner "$GW_UID" -j ACCEPT
    IPT -C OUTPUT -p tcp -d "$ip" --dport 443 -j REJECT --reject-with tcp-reset 2>/dev/null || \
      IPT -A OUTPUT -p tcp -d "$ip" --dport 443 -j REJECT --reject-with tcp-reset
  done
  IPT -C OUTPUT -d 203.107.1.1 -p udp -j DROP 2>/dev/null || IPT -A OUTPUT -d 203.107.1.1 -p udp -j DROP
  IPT -C OUTPUT -d 203.107.1.1 -p tcp -j DROP 2>/dev/null || IPT -A OUTPUT -d 203.107.1.1 -p tcp -j DROP
  echo "firewall $1 ok: guarded=($CP_IPS required_direct=($REQ_DIRECT)) gw_user=$GW_USER"
}

apply_remove() {
  for ip in $CP_IPS; do
    while IPT -C OUTPUT -p tcp -d "$ip" --dport 443 -m owner --uid-owner "$GW_UID" -j ACCEPT 2>/dev/null; do
      IPT -D OUTPUT -p tcp -d "$ip" --dport 443 -m owner --uid-owner "$GW_UID" -j ACCEPT; done
    while IPT -C OUTPUT -p tcp -d "$ip" --dport 443 -j REJECT --reject-with tcp-reset 2>/dev/null; do
      IPT -D OUTPUT -p tcp -d "$ip" --dport 443 -j REJECT --reject-with tcp-reset; done
  done
  while IPT -C OUTPUT -d 203.107.1.1 -p udp -j DROP 2>/dev/null; do IPT -D OUTPUT -d 203.107.1.1 -p udp -j DROP; done
  while IPT -C OUTPUT -d 203.107.1.1 -p tcp -j DROP 2>/dev/null; do IPT -D OUTPUT -d 203.107.1.1 -p tcp -j DROP; done
  echo "firewall REMOVE ok"
}

case "$ACT" in
  add)      [ -n "$CP_IPS" ] || { echo "无控制面 IP（$CP_FILE 或传参）"; exit 1; }; apply_add lite ;;
  add-full) [ -n "$CP_IPS" ] || { echo "无控制面 IP（$CP_FILE 或传参）"; exit 1; }; apply_add full ;;
  remove)   [ -n "$CP_IPS" ] || { echo "无控制面 IP（$CP_FILE 或传参）"; exit 1; }; apply_remove ;;
  status)
    n=0
    for ip in $CP_IPS; do
      IPT -C OUTPUT -p tcp -d "$ip" --dport 443 -j REJECT --reject-with tcp-reset 2>/dev/null && { echo "guarded: $ip$(is_required "$ip" && echo ' [full-only]')"; n=$((n+1)); }
    done
    IPT -C OUTPUT -d 203.107.1.1 -p udp -j DROP 2>/dev/null && { echo "guarded: 203.107.1.1 httpdns"; n=$((n+1)); }
    echo "active_rules=$n cp_ips=($CP_IPS) required_direct=($REQ_DIRECT)"
    ;;
  *) echo "usage: $0 <add|add-full|remove|status> [cp-ips]"; exit 1 ;;
esac
