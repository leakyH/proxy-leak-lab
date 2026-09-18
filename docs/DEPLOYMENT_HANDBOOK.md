# Proxy Leak Lab 部署手册

> 服务器：阿里云 Linux 3 (Alibaba Cloud Linux 3)，x86_64，公网 IP `203.0.113.20`
> 域名：`example.com`（Cloudflare DNS 管理）
> 部署时间：2026-07-01

---

## 1. 架构概览

```
用户浏览器 ──HTTPS──→ Cloudflare DNS (灰云/仅DNS) ──→ 服务器 203.0.113.20
                                                       │
                                                     nginx (80/443)
                                                       │ 反向代理
                                                       ▼
                                               leak-server (127.0.0.1:8080)
                                                       │
                                    ┌──────────────────┼──────────────────┐
                                    ▼                  ▼                  ▼
                               TCP :9001          UDP :9002          UDP :3478
                              (Raw TCP)          (Raw UDP)          (STUN)
```

- **leak-server**：Go 二进制，处理 HTTP API + 原始 TCP/UDP/STUN 协议探测
- **nginx**：HTTPS 反代 + TLS 终止（Let's Encrypt 证书）
- **Cloudflare**：纯 DNS 解析（灰云），不经过 CDN 代理

---

## 2. DNS 配置

### 2.1 为什么必须灰云（仅 DNS）

如果开启 Cloudflare 代理（橙云），用户流量会先到 Cloudflare 边缘节点，服务器看到的 `source_ip` 是 Cloudflare 的 IP 而非用户真实 IP，整个泄露检测就失效了。

### 2.2 记录清单

在 Cloudflare DNS 面板创建（全部 **仅 DNS / 灰云**）：

| 类型 | 名称 | 内容 |
|------|------|------|
| A | `leak` | `203.0.113.20` |
| A | `v4` | `203.0.113.20` |
| A | `stun` | `203.0.113.20` |

不需要 AAAA（IPv6），服务器无公网 IPv6。

---

## 3. 安全组（阿里云控制台）

在 ECS 实例的安全组中放行：

| 协议 | 端口 | 用途 |
|------|------|------|
| TCP | 80 | HTTP（nginx，也用于 certbot 续期） |
| TCP | 443 | HTTPS（nginx） |
| TCP | 9001 | 原始 TCP 探测 |
| UDP | 9002 | 原始 UDP 探测 |
| UDP | 3478 | STUN 绑定探测 |

本机 `firewalld` 未运行，无需额外配置。

---

## 4. leak-server 部署

### 4.1 为什么不按 Docker Compose 方案

1. **Docker Hub 被墙**：阿里云服务器无法访问 `registry-1.docker.io`，拉不到 `golang` 和 `caddy` 镜像
2. **预编译二进制 GLIBC 不兼容**：zip 中 `bin/leak-server` 需要 GLIBC 2.34，系统只有 2.32
3. 方案：用系统 Go 1.25.10 **从源码静态编译**（`CGO_ENABLED=0`），无外部依赖

### 4.2 编译

```bash
# 解压源码
sudo unzip -q proxy-leak-lab.zip -d /opt/proxy-leak-lab/releases/$(date -u +%Y%m%d-%H%M%S)

# 编译（静态链接）
cd /opt/proxy-leak-lab/releases/20260701-132259/proxy-leak-lab
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /opt/proxy-leak-lab/leak-server ./server
```

### 4.3 配置文件

`/opt/proxy-leak-lab/leak-server.env`（权限 600）：

```ini
BASE_DOMAIN=example.com
DASHBOARD_TOKEN=<生成：openssl rand -hex 24>
HTTP_ADDR=127.0.0.1:8080      # 仅本地，不暴露公网
TCP_ADDR=:9001
UDP_ADDR=:9002
STUN_ADDR=:3478
DATA_FILE=/opt/proxy-leak-lab/data/events.jsonl
GEOIP_DB=/usr/share/GeoIP/GeoLite2-Country.mmdb
GEOIP_LOOKUP_BIN=/usr/bin/mmdblookup
```

关键安全约束：
- `HTTP_ADDR` 必须绑定 `127.0.0.1`，不能是 `0.0.0.0`
- 环境文件权限 600，防止其他用户读取 Dashboard Token

### 4.4 systemd 服务

`/etc/systemd/system/leak-server.service`：

```ini
[Unit]
Description=Proxy Leak Lab Server
After=network.target

[Service]
Type=simple
User=admin
Group=admin
EnvironmentFile=/opt/proxy-leak-lab/leak-server.env
ExecStart=/opt/proxy-leak-lab/leak-server
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/proxy-leak-lab/data
ReadOnlyPaths=/usr/share/GeoIP

[Install]
WantedBy=multi-user.target
```

- `AmbientCapabilities=CAP_NET_BIND_SERVICE`：允许非 root 用户绑定 3478（<1024）端口
- `ProtectSystem=strict` + `ReadWritePaths`：最小权限，只能写数据目录

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now leak-server
```

---

## 5. TLS 证书

### 5.1 为什么没用 Caddy 的自动 TLS

Caddy 默认用 HTTP-01 挑战（需 80 端口）。本机 80 已被 nginx 占用，所以改用 certbot + Cloudflare DNS-01 挑战。

### 5.2 certbot 安装与配置

```bash
sudo yum install -y certbot python3-certbot-dns-cloudflare
```

Cloudflare API Token 凭证文件 `/etc/letsencrypt/cloudflare.ini`（权限 600）：

```ini
dns_cloudflare_api_token = cfut_xxxxxxxxxxxxxxxx
```

> Token 在 Cloudflare 面板 → My Profile → API Tokens → Create Token → Edit zone DNS 模板，限定到目标域名。

### 5.3 签发证书

```bash
# 首次
sudo certbot certonly --dns-cloudflare \
  --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
  -d leak.example.com -d v4.example.com \
  --non-interactive --agree-tos --email admin@example.com

# 后续添加域名
sudo certbot certonly --dns-cloudflare \
  --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
  -d leak.example.com -d v4.example.com --expand
```

DNS-01 工作原理：certbot 通过 Cloudflare API 在域名的 TXT 记录中写入 `_acme-challenge`，Let's Encrypt 查询该记录验证域名所有权，验证完成后 certbot 删除临时记录。全程不需要服务器开放任何端口。

证书位置：`/etc/letsencrypt/live/leak.example.com/`
- `fullchain.pem` — 完整证书链
- `privkey.pem` — 私钥

自动续期：certbot 安装时已注册 systemd timer，到期前自动续期。

---

## 6. nginx 配置

### 6.1 为什么是 nginx 而非 Caddy

1. 本机已有 nginx 在运行（80 端口）
2. Docker Hub 不可达，无法拉取带 Cloudflare 插件的 Caddy 镜像
3. nginx 完全满足需求：HTTP/2、反向代理、TLS 终止

### 6.2 完整配置

`/etc/nginx/conf.d/leak.conf`：

```nginx
# HTTP → HTTPS 重定向
server {
    listen 80;
    server_name leak.example.com v4.example.com;
    return 301 https://$host$request_uri;
}

# HTTPS 主站
server {
    listen 443 ssl http2;
    server_name leak.example.com v4.example.com;

    ssl_certificate     /etc/letsencrypt/live/leak.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/leak.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host              $host;
        proxy_set_header X-Edge-Remote-IP  $remote_addr;
        proxy_set_header X-Edge-Remote-Port $remote_port;
        proxy_set_header X-Edge-Protocol   $server_protocol;
        proxy_set_header X-Edge-Scheme     $scheme;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    }
}
```

关键点：
- `proxy_pass http://127.0.0.1:8080` — 只代理到本地，leak-server 不直接暴露
- `X-Edge-Remote-IP` 传递真实客户端 IP（来自 TCP 连接，非信任头）
- `return 301` 强制 HTTP 跳转 HTTPS，避免明文传输 Dashboard Token

---

## 7. 客户端编译

预编译的 `leak-client-linux-amd64` 同样有 GLIBC 2.34 问题。从源码编译：

```bash
cd /opt/proxy-leak-lab/releases/20260701-132259/proxy-leak-lab
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/leak-client ./client
```

使用示例：

```bash
# 直连测试
/tmp/leak-client -domain example.com -test-id my-test

# 通过 HTTP 代理
/tmp/leak-client -domain example.com -test-id proxy-test \
  -http-proxy http://127.0.0.1:7890

# 通过 SOCKS5
/tmp/leak-client -domain example.com -test-id socks-test \
  -socks5 socks5://127.0.0.1:1080

# 只走代理，跳过直连
/tmp/leak-client -domain example.com -test-id proxy-only \
  -socks5 socks5://127.0.0.1:1080 -skip-direct
```

---

## 8. 维护命令速查

```bash
# 服务状态
systemctl status leak-server nginx

# 查看日志
sudo journalctl -u leak-server -f

# 查看事件数据
cat /opt/proxy-leak-lab/data/events.jsonl

# 手动续期证书
sudo certbot renew --dry-run   # 测试
sudo certbot renew             # 执行

# 重启服务
sudo systemctl restart leak-server
sudo systemctl reload nginx

# 回滚到旧版本
ls /opt/proxy-leak-lab/releases/
# 切换到旧版本：
# sudo cp /opt/proxy-leak-lab/releases/<old>/proxy-leak-lab/bin/leak-server /opt/proxy-leak-lab/leak-server
# sudo systemctl restart leak-server
```

---

## 9. 当前局限

| 项目 | 说明 |
|------|------|
| HTTP/3 (QUIC) | nginx 主线不支持。原项目用 Caddy 发布 UDP 443，当前部署未实现 |
| GeoIP | 需 MaxMind 账号凭据，当前 `database_unavailable` |
| v6 域名 | 服务器无公网 IPv6，`v6.example.com` 故意未创建 |
| STUN 无 test_id | 浏览器 STUN 请求不携带 test_id，靠时间戳+ICE candidate 关联 |
