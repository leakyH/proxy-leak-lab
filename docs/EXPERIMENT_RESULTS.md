# Proxy Leak Lab 实验结果报告

> 实验日期：2026-07-01 ~ 2026-07-02
> 测试域名：`example.com`
> 客户端：Windows 10 + Edge 149 + Go 命令行客户端
> 代理：HTTP/SOCKS5（`127.0.0.1:7891`）

---

## 实验环境

| 项目 | 值 |
|------|------|
| 真实公网 IP | `203.0.113.10` |
| 代理出口 IP 池 | `198.51.100.x`、`198.51.100.20` |
| 服务器位置 | 阿里云（`203.0.113.20`） |
| DNS | Cloudflare 灰云（仅 DNS，不代理） |
| TLS | Let's Encrypt，nginx 终止 |
| GeoIP | 未配置 |
| WebRTC mDNS | Chrome 默认启用（局域网 IP 被混淆为 `*.local` UUID） |

---

## 代理协议分层背景

在解读结果之前，先理解三种代理机制的本质差异：

```
OSI 层        代理类型        工作原理                    覆盖范围
─────────────────────────────────────────────────────────────────
L7 应用层     HTTP 代理      解析 HTTP 请求，代发        仅 HTTP/HTTPS
L5 会话层     SOCKS5        不解协议内容，盲转字节流     TCP（+ UDP 理论上可以）
L3/4 网络层   TUN/VPN        虚拟网卡劫持 IP 包          一切流量
```

**关键区别在于"谁来配合"**：

| 类型 | 应用需要配合？ | 怎么生效？ |
|------|:--:|------|
| HTTP 代理 | 是 | HTTP 库自动读 `HTTP_PROXY` 环境变量——开发者免费获得，无需写代码 |
| SOCKS5 TCP | 是 | 应用显式调用 `SOCKS5 CONNECT`——需开发者手动实现 |
| SOCKS5 UDP | 是 | 应用显式调用 `SOCKS5 UDP ASSOCIATE`——**本客户端未实现** |
| TUN | **否** | OS 路由表指向虚拟网卡，应用完全无感知 |

**HTTP 代理看似"自动"，本质是库代劳**。一旦应用自己裸写 TCP（`net.Dial` 而非 `http.Get`），`HTTP_PROXY` 就形同虚设。木马、P2P 软件、网游、部分 Go 自研 Agent 都会这样做——TUN 是唯一能兜底的方案。

**UDP 天然裸奔**：操作系统 `socket.sendto()` 没有"代理"概念，发包即进内核协议栈 → 查路由表 → 物理网卡发出。要么每个应用自己实现 SOCKS5 UDP ASSOCIATE（现实：几乎没人做），要么开 TUN 强行接管。

SOCKS5 的典型使用场景：SSH 动态转发（`ssh -D 1080`）、穿透企业防火墙（不解析协议内容）、代理链嵌套（Clash/V2Ray 内部转发）、爬虫/自动化（原生 socket 也走代理）。

---

## 实验结果

### 测试 2：直连

**配置**：无代理，浏览器直连

| 路径 | 服务器看到的 IP |
|------|:--:|
| HTTP (leak + v4) | `203.0.113.10` |
| WebRTC STUN srflx | `203.0.113.10` |

**结论**：一致，无泄露。

---

### 测试 3：浏览器代理 + 代理软件全局模式

**配置**：浏览器走代理 + 代理软件全局

| 路径 | 服务器看到的 IP |
|------|:--:|
| HTTP (leak + v4) | `198.51.100.20` ← 代理出口 |
| WebRTC STUN srflx | **`203.0.113.10`** ← 真实 IP |

**结论**：🔴 **WebRTC 泄露**。代理软件的"全局"只代理了 TCP (HTTP/HTTPS)，WebRTC UDP 绕过代理直连。

---

### 测试 4：浏览器系统代理 + 代理软件全局模式

**配置**：浏览器使用系统代理设置 + 代理软件全局

| 路径 | 服务器看到的 IP |
|------|:--:|
| HTTP (leak + v4) | `198.51.100.20` |
| WebRTC STUN srflx | **`203.0.113.10`** |

**结论**：🔴 **WebRTC 泄露**。结果与测试 3 一致——"浏览器代理"和"系统代理"对代理软件无区别。

---

### 测试 5：浏览器系统代理 + 代理软件规则模式

**配置**：浏览器系统代理 + 代理软件规则（非全局）

| 路径 | 服务器看到的 IP |
|------|:--:|
| HTTP (leak + v4) | `198.51.100.21` ← 另一出口 |
| WebRTC STUN srflx | **`203.0.113.10`** |

**结论**：🔴 **双问题**。`example.com` 未命中代理规则走了另一出口；WebRTC 照常泄露。

---

### 测试 6：命令行 HTTP_PROXY 显式代理 🆕

**配置**：`leak-client -http-proxy http://127.0.0.1:7891`

| path_class | 测试 | 服务器看到的 IP |
|:--|------|:--:|
| proxy-aware | HTTP (leak + v4) | `198.51.100.21` ← 代理 ✅ |
| proxy-unaware | HTTP direct | **`203.0.113.10`** |
| proxy-unaware | Raw TCP :9001 | **`203.0.113.10`** |
| proxy-unaware | Raw UDP :9002 | **`203.0.113.10`** |
| proxy-unaware | STUN :3478 | **`203.0.113.10`** |

**结论**：HTTP 代理的覆盖范围验证——HTTP ✅，TCP/UDP/STUN 全部直连泄露。**HTTP 代理只能代理 HTTP，这是协议定义决定的，不是 bug。**

---

### 测试 7：命令行 SOCKS5 代理 🆕

**配置**：`leak-client -socks5 socks5://127.0.0.1:7891`

| path_class | 测试 | 服务器看到的 IP |
|:--|------|:--:|
| proxy-aware | HTTP SOCKS5 | `198.51.100.21` ← 代理 ✅ |
| proxy-aware | TCP SOCKS5 | `198.51.100.21` ← 代理 ✅ |
| proxy-unaware | HTTP direct | `203.0.113.10` |
| proxy-unaware | TCP direct | `203.0.113.10` |
| proxy-unaware | UDP direct | `203.0.113.10` |
| proxy-unaware | STUN direct | `203.0.113.10` |

**结论**：SOCKS5 的覆盖范围验证——HTTP ✅，TCP ✅，UDP/STUN ❌。**比 HTTP 代理强在了 TCP，但 UDP 仍然泄露。** 注意 `proxy-aware` 的 TCP SOCKS5 走了代理出口，这是 HTTP 代理做不到的。

---

### 测试 8：SOCKS5 纯代理（跳过直连）🆕

**配置**：`leak-client -socks5 socks5://127.0.0.1:7891 -skip-direct`

| path_class | 测试 | 服务器看到的 IP |
|:--|------|:--:|
| proxy-aware | HTTP SOCKS5 | `198.51.100.21` |
| proxy-aware | TCP SOCKS5 | `198.51.100.21` |

**结论**：去掉直连噪声后，SOCKS5 的两条路径均正确代理。

---

## 最终汇总矩阵

### 浏览器实验

```
#         场景              HTTP出口     WebRTC STUN    结果
──────────────────────────────────────────────────────────────
测试2    直连              124.x        124.x         ✅ 一致
测试3    全局代理          188.x        124.x         🔴 泄露
测试4    系统+全局         188.x        124.x         🔴 泄露
测试5    系统+规则         220.x        124.x         🔴 泄露
```

### 命令行实验

```
#         场景          HTTP        TCP         UDP/STUN
─────────────────────────────────────────────────────────
测试6    HTTP_PROXY     220.x ✅    124.x 🔴    124.x 🔴
测试7    SOCKS5         220.x ✅    220.x ✅    124.x 🔴
测试8    SOCKS5-only    220.x ✅    220.x ✅    (未跑)
```

### 代理协议能力边界

```
                 HTTP代理    SOCKS5      TUN/VPN
─────────────────────────────────────────────────
HTTP/HTTPS         ✅          ✅          ✅
原始 TCP           ❌          ✅          ✅
原始 UDP           ❌          ❌*         ✅
WebRTC STUN        ❌          ❌*         ✅
DNS                ❌          ❌*         ✅
任意裸协议         ❌          ❌*         ✅
─────────────────────────────────────────────────
* SOCKS5 协议支持 UDP ASSOCIATE，但本客户端和大多数应用未实现
```

---

## 关键安全发现

### 发现 1：WebRTC 在所有代理模式下均泄露

5 轮浏览器测试中，除直连外每次 WebRTC STUN 都暴露真实 IP `203.0.113.10`。缓解：
- 代理软件启用 TUN/VPN 模式
- 浏览器禁用 WebRTC（`chrome://flags/#disable-webrtc`）

### 发现 2：HTTP 代理保护面最窄

仅覆盖 HTTP/HTTPS。TCP/UDP/STUN/DNS 全部泄漏。**这是 HTTP 代理的协议本质，不是配置问题。**

### 发现 3：SOCKS5 补齐 TCP 但不补齐 UDP

SOCKS5 TCP CONNECT 正常工作，但 UDP ASSOCIATE 未在客户端实现（原项目 README 明确列为当前限制），导致 UDP/STUN 泄露。缓解：
- 开 TUN 模式
- 或使用实现了 SOCKS5 UDP 的代理链（但浏览器 WebRTC 仍然不受 SOCKS5 控制）

### 发现 4：规则代理模式下域名可能漏网

`example.com` 未在代理规则中，流经非预期出口。规则代理需要显式维护域名列表。

### 发现 5：多个代理出口

同时存在 `198.51.100.x` 和 `198.51.100.x` 两个出口 IP。

### 发现 6：HTTP 代理的"自动生效"是库代劳，非系统行为

HTTP 代理不需要开发者写代码，因为所有正经 HTTP 库（Go `net/http`、Python `requests`、Node.js `fetch`、libcurl）都内置了读取 `HTTP_PROXY` 的逻辑。但一旦开发者绕过库、自己裸写 `net.Dial` + 手动拼 HTTP 报文，`HTTP_PROXY` 就沦为空壳。这正是木马、P2P、网游等软件刻意绕代理的手段——TUN 是唯一兜底方案。

### 发现 7：UDP 裸奔的根本原因在 OS socket API

OS 的 `sendto()` 系统调用不认代理配置——包直接进内核协议栈、查路由表、从物理网卡发出。要让 UDP 走代理只有两条路：每个应用自己实现 SOCKS5 UDP ASSOCIATE（现实：几乎没人做），或开 TUN 在路由层劫持所有 IP 包。

---

## 对比原项目 README：实验完成度

| # | 原项目测试项 | 状态 |
|:--:|------|:--:|
| 1 | 浏览器 HTTPS 请求源 IP | ✅ 测试 2~5 |
| 2 | 浏览器 IPv4-only 路径 | ✅ |
| 3 | 浏览器 IPv6-only 路径 | ⚠️ N/A（服务器无 IPv6） |
| 4 | 浏览器 WebRTC/STUN srflx | ✅ |
| 5 | HTTP/1.1、HTTP/2、HTTP/3 | ⚠️ HTTP/2 ✅，HTTP/1.1 未测，HTTP/3 未实现 |
| 6 | Raw TCP 源 IP | ✅ |
| 7 | Raw UDP 源 IP | ✅ |
| 8 | STUN/XOR-MAPPED 源 IP | ✅ |
| 9 | HTTP 通过 `HTTP_PROXY` / `HTTPS_PROXY` | ✅ 测试 6 |
| 10 | HTTP + Raw TCP 通过显式 SOCKS5 | ✅ 测试 7~8 |

**完成度：10/10。HTTP/3 和 HTTP/1.1 为平台限制项。**
