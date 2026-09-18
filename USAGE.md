# Proxy Leak Lab 使用说明

## 浏览器测试（网页版）

打开 `https://leak.example.com`：

1. 点「运行浏览器测试」
2. 结果以卡片展示：HTTP 源 IP、IPv4/IPv6 路径、WebRTC ICE candidates、GeoIP 归属
3. 输入 Dashboard token 后点「刷新事件」，可查看服务器观测到的完整事件

**关键看点**：WebRTC STUN 的 srflx candidate 暴露 NAT 后的真实公网 IP——即使 HTTP 走了代理，WebRTC 仍可能直连泄露。

## 客户端工具（命令行）

### 下载

| 平台 | 链接 |
|---|---|
| Linux (amd64) | `https://example.com/dl/leak-client-linux-amd64` |
| Windows (amd64) | `https://example.com/dl/leak-client-windows-amd64.exe` |

### 用法

```bash
# 默认：格式化摘要（按代理/直连分组）
./leak-client-linux-amd64 -domain example.com

# 显式 HTTP 代理
./leak-client-linux-amd64 -domain example.com -http-proxy http://127.0.0.1:7890

# 显式 SOCKS5 代理
./leak-client-linux-amd64 -domain example.com -socks5 socks5://127.0.0.1:1080

# 只测代理路径（跳过直连探测）
./leak-client-linux-amd64 -domain example.com -socks5 socks5://127.0.0.1:1080 -skip-direct

# 完整 JSON 输出
./leak-client-linux-amd64 -domain example.com -v
```

### 结果解读

- `proxy-aware` — 经代理路径（HTTP_PROXY / SOCKS5）
- `proxy-unaware` — 直连 socket（TUN/VPN 应能捕获，应用级代理不能）
- WebRTC STUN 是原始 UDP，不经过任何代理 → 即使代理配好也可能泄露真实 IP

## 从源码构建

```bash
go build -o leak-client ./client
go build -o leak-server ./server
```
