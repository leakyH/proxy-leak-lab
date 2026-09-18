# Proxy Leak Lab：服务器端 Agent 部署与验收指令

> 本文档用于交给具有服务器终端权限的 Agent。Agent 应按本文执行，不应仅复述步骤。目标是把 `proxy-leak-lab.zip` 安全部署到一台 Linux 公网服务器，完成 HTTPS、HTTP/3、原始 TCP/UDP、STUN 与本地 GeoIP 国家查询，并提交可核验的部署报告。

---

## 1. 任务目标

部署完成后，应具备以下能力：

- `https://leak.<BASE_DOMAIN>`：浏览器检查页和 Dashboard。
- `https://v4.<BASE_DOMAIN>`：仅 IPv4 的 HTTPS 测试入口。
- `https://v6.<BASE_DOMAIN>`：仅 IPv6 的 HTTPS 测试入口；服务器无公网 IPv6 时可不部署。
- TCP `9001`：记录原始 TCP 连接的服务器观察源 IP。
- UDP `9002`：记录原始 UDP 数据报的服务器观察源 IP。
- UDP `3478`：STUN Binding 测试，返回并记录 NAT 映射地址。
- TCP/UDP `443`：HTTPS 及 HTTP/3/QUIC。
- 所有服务器观察到的公网源 IP均附带 MaxMind GeoLite2 国家代码和国家名。
- 私网、环回、链路本地、无效或数据库无记录的地址必须显示明确状态，不得猜测国家。

该系统用于检测代理、VPN、TUN、IPv4/IPv6、UDP/WebRTC 等路径分别向服务器暴露了哪个出口地址。

---

## 2. Agent 应获得的输入

### 2.1 必需文件

首选输入：

```text
proxy-leak-lab.zip
```

压缩包内包含：

```text
proxy-leak-lab/
├── server/                 Go 服务端源代码
├── client/                 Go 本地测试客户端源代码
├── web/                    浏览器页面
├── caddy/Caddyfile         HTTPS/HTTP3 入口配置
├── docker-compose.yml
├── Dockerfile
├── .env.example
├── go.mod
├── Makefile
├── README.md
└── bin/                    预编译二进制
```

部署时应优先使用压缩包中的源代码和 Dockerfile 构建，不要盲目直接执行预编译二进制。

### 2.2 必需配置

Agent 需要取得以下值；缺失时应报告“阻塞项”，不得自行伪造：

```dotenv
BASE_DOMAIN=example.com
ACME_EMAIL=admin@example.com
DASHBOARD_TOKEN=<至少 48 个十六进制字符的随机值>
MAXMIND_ACCOUNT_ID=<MaxMind 账户 ID>
MAXMIND_LICENSE_KEY=<MaxMind GeoLite2 许可证密钥>
GEOIPUPDATE_FREQUENCY=72
```

敏感值不得出现在终端回显、聊天报告、日志截图或提交记录中。

### 2.3 DNS 条件

应由 DNS 管理员预先创建：

| 主机名 | 记录 | 值 |
|---|---|---|
| `leak.<BASE_DOMAIN>` | A | 服务器公网 IPv4 |
| `leak.<BASE_DOMAIN>` | AAAA | 服务器公网 IPv6，可选 |
| `v4.<BASE_DOMAIN>` | A | 服务器公网 IPv4 |
| `v6.<BASE_DOMAIN>` | AAAA | 服务器公网 IPv6，可选 |
| `stun.<BASE_DOMAIN>` | A | 服务器公网 IPv4 |
| `stun.<BASE_DOMAIN>` | AAAA | 服务器公网 IPv6，可选 |

第一轮代理泄漏测试必须让 DNS 直接指向源服务器，不应启用 Cloudflare 橙云、CDN、外部负载均衡或其他反向代理，否则服务器首先看到的是中间节点 IP。

---

## 3. 不可违反的约束

1. 不得将 `.env`、Dashboard Token、MaxMind License Key 上传到仓库或粘贴到报告。
2. 不得把应用 HTTP 后端 `127.0.0.1:8080` 改成公网监听。
3. 不得无条件信任客户端自行提交的 `X-Forwarded-For`、`Forwarded` 或 `X-Real-IP`。
4. 不得在首轮验收时把站点放到 CDN 后方。
5. 不得开放通用代理、开放 DNS 递归或无认证 TURN 中继。
6. 不得记录 Cookie、Authorization、请求正文或其他与测试无关的敏感内容。
7. 不得为了“让测试通过”而关闭宿主机现有安全策略；需要改防火墙时只开放本文列明的端口。
8. 不得删除旧版本。部署应保留可回滚副本。
9. GeoIP 只表示 IP 前缀的估计国家，不得描述为用户精确位置。

---

## 4. 推荐目录布局

使用版本化发布目录，便于回滚：

```text
/opt/proxy-leak-lab/
├── current -> releases/20260701-120000
├── releases/
│   └── 20260701-120000/
│       └── proxy-leak-lab/
└── backups/
```

创建目录：

```bash
sudo install -d -m 0750 /opt/proxy-leak-lab/releases
sudo install -d -m 0750 /opt/proxy-leak-lab/backups
```

不要在 `/tmp` 中作为长期运行目录启动服务。

---

## 5. 执行步骤

### 步骤 1：确认系统和权限

执行并记录非敏感输出：

```bash
uname -a
cat /etc/os-release
uname -m
id
df -h /
free -h
```

要求：

- Linux 服务器；推荐 Ubuntu。
- 推荐 `amd64/x86_64`，但 Docker 从源代码构建也可支持其他 Go 架构。
- 至少约 2 GB 可用磁盘空间。
- 能使用 `sudo` 或 root。

### 步骤 2：检查 Docker

```bash
docker version
docker compose version
```

如果未安装，只有在得到管理员允许后才能按该发行版的 Docker 官方方式安装 Docker Engine 和 Compose 插件。不要用来历不明的一键脚本。

### 步骤 3：校验输入文件

在收到文件的目录执行：

```bash
sha256sum proxy-leak-lab.zip
unzip -t proxy-leak-lab.zip
```

当前交付压缩包的 SHA-256 应以随文件提供的最新版 `proxy-leak-lab-checksums.txt` 为准。若校验文件与实际压缩包不一致，停止部署并报告；不要忽略。

随后列出内容，确认不存在绝对路径或 `../` 路径穿越：

```bash
zipinfo -1 proxy-leak-lab.zip | sed -n '1,200p'
```

### 步骤 4：检查端口占用

```bash
sudo ss -lntup | grep -E ':(80|443|8080|3478|9001|9002)\b' || true
```

预期公网端口：

```text
TCP 80, 443, 9001
UDP 443, 3478, 9002
```

`8080` 只能由应用监听在 `127.0.0.1`。如果 80/443 已被 Nginx、Apache 或其他 Caddy 占用，不要直接停止生产服务；先报告冲突并提出迁移或合并入口方案。

### 步骤 5：检查 DNS

将域名替换为实际值：

```bash
dig +short A leak.example.com
dig +short A v4.example.com
dig +short AAAA v6.example.com
dig +short A stun.example.com
```

可进一步从服务器外的解析器确认：

```bash
dig @1.1.1.1 +short A leak.example.com
dig @8.8.8.8 +short A leak.example.com
```

验收条件：

- `leak` 和 `v4` 的 A 记录直接返回本服务器公网 IPv4。
- `v6` 只有在服务器确实拥有可用公网 IPv6 时才设置 AAAA。
- 不得解析到 CDN 地址。

### 步骤 6：解压到新发布目录

```bash
release="$(date -u +%Y%m%d-%H%M%S)"
sudo install -d -m 0750 "/opt/proxy-leak-lab/releases/$release"
sudo unzip -q proxy-leak-lab.zip -d "/opt/proxy-leak-lab/releases/$release"
cd "/opt/proxy-leak-lab/releases/$release/proxy-leak-lab"
```

在运行前检查核心配置：

```bash
sed -n '1,240p' docker-compose.yml
sed -n '1,240p' Dockerfile
sed -n '1,240p' caddy/Caddyfile
sed -n '1,240p' .env.example
```

重点确认：

- `app` 使用 `network_mode: host`。
- HTTP 后端为 `127.0.0.1:8080`。
- TCP/UDP/STUN 分别为 `9001/9002/3478`。
- GeoIP 数据库通过只读卷挂载到应用。
- Caddy 仅反向代理到 `127.0.0.1:8080`。
- Caddy 开启 `h1 h2 h3`。

### 步骤 7：创建安全的 `.env`

先限制 umask：

```bash
umask 077
cp .env.example .env
```

如果尚未提供 Dashboard Token，可由 Agent 在服务器上生成，但只能写入 `.env`，不得在报告中显示：

```bash
openssl rand -hex 24
```

安全编辑 `.env`：

```bash
nano .env
chmod 600 .env
```

检查变量是否存在，但不要打印值：

```bash
for key in BASE_DOMAIN ACME_EMAIL DASHBOARD_TOKEN MAXMIND_ACCOUNT_ID MAXMIND_LICENSE_KEY; do
  grep -q "^${key}=." .env || echo "MISSING: $key"
done
```

不得执行 `cat .env` 并把输出发送给用户。

### 步骤 8：验证 Compose 配置

```bash
docker compose config --quiet
```

如需检查展开后的配置，应先确保输出不会泄露 `.env` 中的敏感值。不要把完整 `docker compose config` 输出粘贴到聊天中。

### 步骤 9：配置防火墙

Ubuntu/UFW 示例：

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 443/udp
sudo ufw allow 9001/tcp
sudo ufw allow 9002/udp
sudo ufw allow 3478/udp
sudo ufw status numbered
```

同时检查云厂商安全组。只在本项目确实需要时开放这些端口。

### 步骤 10：构建并启动

```bash
docker compose pull
docker compose build --pull
docker compose up -d
```

然后建立 `current` 软链接：

```bash
sudo ln -sfn "/opt/proxy-leak-lab/releases/$release" /opt/proxy-leak-lab/current
```

查看容器状态：

```bash
docker compose ps
```

查看日志时必须注意脱敏：

```bash
docker compose logs --tail=200 app caddy geoipupdate
```

### 步骤 11：确认监听状态

```bash
sudo ss -lntup | grep -E ':(80|443|8080|3478|9001|9002)\b'
```

预期：

- Caddy：TCP 80、TCP 443、UDP 443。
- app：`127.0.0.1:8080`。
- app：TCP 9001、UDP 9002、UDP 3478。

如果 `8080` 显示为 `0.0.0.0:8080` 或 `[::]:8080`，判定为配置错误，立即停止并修正。

### 步骤 12：等待 GeoIP 数据库就绪

```bash
docker compose logs --tail=200 geoipupdate
docker compose exec app sh -c 'test -s /usr/share/GeoIP/GeoLite2-Country.mmdb && ls -lh /usr/share/GeoIP/GeoLite2-Country.mmdb'
```

若数据库未下载：

- 检查 MaxMind Account ID 和 License Key 是否有效，但不得打印密钥。
- 检查服务器是否可访问 MaxMind 下载端点。
- 检查 `geoip_data` 卷挂载。
- 应用允许在数据库未就绪时运行，但验收状态应标为“部分完成”。

### 步骤 13：本机健康检查

```bash
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/api/config
```

预期：

- `/healthz` 返回 `204`。
- `/api/config` 返回 JSON。
- GeoIP 状态最终应为 ready 或等价状态。

### 步骤 14：公网 HTTPS 检查

```bash
curl -fsS -I "https://leak.${BASE_DOMAIN}/"
curl -fsS "https://leak.${BASE_DOMAIN}/api/config"
curl -fsS "https://v4.${BASE_DOMAIN}/api/http?test_id=server-agent-v4&label=server-agent-v4"
```

若有 IPv6：

```bash
curl -6 -fsS "https://v6.${BASE_DOMAIN}/api/http?test_id=server-agent-v6&label=server-agent-v6"
```

检查证书：

```bash
openssl s_client -connect "leak.${BASE_DOMAIN}:443" -servername "leak.${BASE_DOMAIN}" </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates
```

### 步骤 15：HTTP/3 检查

本机 `curl` 只有在编译时支持 HTTP/3 才能执行：

```bash
curl --version
curl -v --http3-only "https://leak.${BASE_DOMAIN}/api/http?test_id=server-agent-h3&label=server-agent-h3"
```

若 curl 不支持 HTTP/3，不得把它误报为服务失败。应通过以下方式辅助确认：

```bash
docker compose logs --tail=200 caddy
sudo ss -lunp | grep ':443\b'
```

最终仍建议由支持 HTTP/3 的外部浏览器或客户端验证。

### 步骤 16：原始 TCP/UDP/STUN 验证

优先使用压缩包中的 Linux 客户端从另一台机器执行：

```bash
chmod +x leak-client-linux-amd64
./leak-client-linux-amd64 -domain example.com -test-id agent-external-test
```

不要只从服务器自身运行，因为那只能验证监听器，不足以验证公网源 IP。

从 Windows 测试机：

```powershell
.\leak-client-windows-amd64.exe -domain example.com -test-id agent-external-test
```

需要对比显式代理时：

```powershell
.\leak-client-windows-amd64.exe `
  -domain example.com `
  -test-id agent-proxy-test `
  -http-proxy http://127.0.0.1:7890
```

或：

```powershell
.\leak-client-windows-amd64.exe `
  -domain example.com `
  -test-id agent-socks-test `
  -socks5 socks5://127.0.0.1:1080
```

### 步骤 17：Dashboard 验证

打开：

```text
https://leak.<BASE_DOMAIN>/?test_id=agent-external-test
```

输入 Dashboard Token，确认：

- HTTP、TCP、UDP、STUN 事件可以读取。
- 每个公网 `source_ip` 后显示国家名称和 ISO 国家代码。
- 私网、环回等地址显示类别而非国家猜测。
- HTTP 事件显示协议版本。
- Dashboard 未公开显示 Token、MaxMind Key、Cookie 或 Authorization。

事件接口要求 Token，例如：

```bash
curl -fsS \
  -H "X-Lab-Token: $DASHBOARD_TOKEN" \
  "https://leak.${BASE_DOMAIN}/api/events?test_id=agent-external-test"
```

不要把包含完整公网 IP 的原始事件日志公开发布。

---

## 6. 部署成功判定

只有同时满足以下项目，才可报告“完成”：

- [ ] Docker Compose 三个服务均在运行：`app`、`caddy`、`geoipupdate`。
- [ ] `leak.<domain>` HTTPS 证书有效。
- [ ] `v4.<domain>` 可经 IPv4 访问。
- [ ] 有公网 IPv6 时，`v6.<domain>` 可经 IPv6 访问；没有 IPv6 时明确标记“不适用”。
- [ ] TCP 9001、UDP 9002、UDP 3478 对外可达。
- [ ] UDP 443 正在监听；HTTP/3 已验证或明确说明客户端工具不支持验证。
- [ ] `/healthz` 返回 204。
- [ ] GeoLite2-Country 数据库已下载且应用状态为 ready。
- [ ] Dashboard 能看到服务器观察 IP 及国家代码/国家名。
- [ ] `127.0.0.1:8080` 未对公网开放。
- [ ] `.env` 权限为 600，报告中没有泄露任何密钥。
- [ ] 已保留当前发布路径和回滚方式。

---

## 7. Agent 最终报告格式

Agent 完成后应按以下格式汇报，所有密钥必须写成 `[REDACTED]`：

```text
Proxy Leak Lab 部署报告

总体状态：完成 / 部分完成 / 失败
发布目录：/opt/proxy-leak-lab/releases/<timestamp>
当前链接：/opt/proxy-leak-lab/current

域名：
- leak.<domain>: 正常 / 异常
- v4.<domain>: 正常 / 异常
- v6.<domain>: 正常 / 不适用 / 异常
- stun.<domain>: DNS 正常 / 异常

服务：
- app: running / error
- caddy: running / error
- geoipupdate: running / error

监听：
- TCP 80: OK/FAIL
- TCP 443: OK/FAIL
- UDP 443: OK/FAIL
- TCP 9001: OK/FAIL
- UDP 9002: OK/FAIL
- UDP 3478: OK/FAIL
- HTTP 127.0.0.1:8080 only: YES/NO

TLS：
- 证书主体：<非敏感信息>
- 到期时间：<date>

GeoIP：
- 数据库：ready/not ready
- 数据库文件更新时间：<date>
- 公网测试 IP 国家字段：正常/异常

外部协议验收：
- HTTPS: OK/FAIL
- IPv4-only: OK/FAIL
- IPv6-only: OK/不适用/FAIL
- Raw TCP: OK/FAIL
- Raw UDP: OK/FAIL
- STUN: OK/FAIL
- HTTP/3: OK/未验证/FAIL

安全检查：
- .env 权限 600: YES/NO
- 8080 未公网暴露: YES/NO
- 未启用 CDN: YES/NO
- 报告未包含 Token/License Key: YES/NO

未完成事项：
- <逐项列出，不能隐瞒>

建议的下一步：
- <只列与当前阻塞直接相关的动作>
```

---

## 8. 常见问题处理

### 8.1 Caddy 无法签发证书

检查：

```bash
dig +short A leak.example.com
sudo ss -lntp | grep -E ':(80|443)\b'
docker compose logs --tail=300 caddy
```

常见原因：

- DNS 尚未生效。
- A/AAAA 指向错误地址。
- 80/443 被其他服务占用。
- 云安全组未开放。
- AAAA 存在但服务器 IPv6 不可达。
- 域名位于 CDN 代理后。

如果 AAAA 错误，优先删除错误 AAAA，而不是强行禁用 TLS 验证。

### 8.2 GeoIP 一直未就绪

```bash
docker compose logs --tail=300 geoipupdate
docker volume ls | grep geoip
docker compose exec app ls -la /usr/share/GeoIP
```

检查 MaxMind 凭据和下载访问。不得把许可证密钥打印到报告。

### 8.3 页面正常，但 TCP/UDP/STUN 无事件

检查：

```bash
sudo ss -lntup | grep -E ':(3478|9001|9002)\b'
sudo tcpdump -ni any 'tcp port 9001 or udp port 9002 or udp port 3478'
```

如果抓不到包，多数是云安全组、宿主机防火墙、客户端代理规则或运营商过滤问题；如果能抓到包但应用无事件，再检查 app 日志。

### 8.4 HTTP 显示的是 CDN 国家

说明请求经过了 CDN 或其他反向代理。第一轮测试应关闭代理并让 DNS 直连服务器。不要靠读取未经验证的 `X-Forwarded-For` 冒充真实源 IP。

### 8.5 IPv6 测试失败

先检查：

```bash
ip -6 addr
ip -6 route
curl -6 https://ifconfig.co/ip
```

如果服务器没有可路由公网 IPv6，应删除 `v6`/AAAA 记录，并将 IPv6 项标记为“不适用”，不要伪造支持。

---

## 9. 升级与回滚

### 升级

每次新压缩包都解压到新的时间戳目录，不应覆盖当前版本：

```bash
release="$(date -u +%Y%m%d-%H%M%S)"
# 解压、创建 .env、docker compose config --quiet、build、up、验收
sudo ln -sfn "/opt/proxy-leak-lab/releases/$release" /opt/proxy-leak-lab/current
```

应用数据、GeoIP 和 Caddy 证书位于 Docker named volumes 中，`docker compose down` 时不得加 `-v`，除非用户明确要求清除数据。

### 回滚

确定上一版本目录：

```bash
ls -1dt /opt/proxy-leak-lab/releases/*
```

停止当前版本，在上一版本目录启动：

```bash
cd /opt/proxy-leak-lab/releases/<previous>/proxy-leak-lab
docker compose up -d --build
sudo ln -sfn /opt/proxy-leak-lab/releases/<previous> /opt/proxy-leak-lab/current
```

回滚后重新执行健康检查、TLS、监听端口和外部协议测试。

---

## 10. 数据保留与删除

事件日志位于 Docker volume 的 `/data/events.jsonl`，包含 IP 地址和推断国家，属于需要谨慎处理的数据。

导出：

```bash
docker compose exec app cat /data/events.jsonl > events.jsonl
chmod 600 events.jsonl
```

删除前应取得用户明确许可。不要为了重新部署执行：

```bash
docker compose down -v
```

因为这会删除事件数据、GeoIP 数据库卷以及可能的 Caddy 状态。

---

## 11. 可直接粘贴给服务器 Agent 的任务指令

```text
请部署我提供的 proxy-leak-lab.zip。先完整阅读同目录中的 SERVER_AGENT_DEPLOYMENT_GUIDE_CN.md，然后实际执行其中的预检查、校验、版本化解压、.env 安全配置、Docker Compose 构建启动和端到端验收。优先从源代码通过 Dockerfile 构建，不要盲目执行压缩包内的预编译服务端二进制。

必须保证：
1. 首轮测试域名直接解析到源服务器，不经过 CDN；
2. HTTP 后端只监听 127.0.0.1:8080；
3. 开放 TCP 80/443/9001 和 UDP 443/3478/9002；
4. GeoLite2-Country 本地数据库正常下载，所有服务器观察公网 IP 显示国家代码和国家名；
5. .env 权限为 600，任何日志和最终报告都不得泄露 Dashboard Token、MaxMind License Key 或其他密钥；
6. 不删除旧版本或 Docker 数据卷，保留回滚路径；
7. 对 HTTPS、IPv4、可用时的 IPv6、Raw TCP、Raw UDP、STUN、GeoIP 和 HTTP/3 逐项验收；无法验证的项目必须明确说明原因，不能宣称成功。

完成后严格按照文档中的“Agent 最终报告格式”回复，并附上必要的脱敏错误摘要。不要在报告中展示完整公网 IP，可只显示前后部分或用 [REDACTED]。
```
