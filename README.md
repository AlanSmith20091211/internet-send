# Internet Send

> 同一 Wi-Fi / 同一手机热点下，任意设备之间互传文件和文字。**一台电脑运行，其他设备扫码即用，手机不用装 App。**

```
┌──────────────┐  局域网 / 热点   ┌──────────────┐
│ 电脑 A（主机） │◀──────────────▶│ 手机：浏览器扫码 │
│ internet-send│◀──────────────▶│ 电脑 B：浏览器  │
└──────────────┘                └──────────────┘
```

## 功能（v0.1）

- **零安装接入**：主机是一个单文件程序；手机、平板、其他电脑用浏览器打开即可，终端和网页里都有二维码
- **不需要外网**：网页资源全部打包进程序，手机热点没流量也能用
- **文件 + 文字**：多文件、拖拽、Ctrl+V 粘贴截图；文字/链接一键复制
- **点对点 / 广播**：可以发给某一台设备，也可以发给所有在线设备
- **大文件友好**：流式写盘，内存占用恒定；下载支持断点续传（HTTP Range）
- **实时**：设备上下线、新文件到达即时推送，带进度和速度显示
- **可选访问口令**：`-pin auto` 生成 6 位口令，二维码自动带口令，扫码即登录；错误 5 次锁定 5 分钟
- **跨平台**：Windows / macOS / Linux（x64、ARM64）

## 快速开始

### 从源码运行（需要 Go 1.22+）

```bash
go run .
# 或安装到 $GOPATH/bin
go install github.com/AlanSmith20091211/internet-send@latest
internet-send
```

启动后终端会显示：

```
  Internet Send v0.1.0 — 局域网文件传输

  本机打开:   http://localhost:8787
  其他设备:   http://192.168.1.23:8787
  文件保存到: /Users/you/Downloads/InternetSend

  用手机扫码打开（需连接同一网络）：
  █▀▀▀▀▀█ ...
```

1. 电脑浏览器打开 `http://localhost:8787`
2. 手机连同一个 Wi-Fi / 热点，扫终端里的二维码（或网页右上角「扫码连接」）
3. 选好「发送给」的设备，选文件或输入文字，发送

### 命令行参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-port` | `8787` | 监听端口；默认端口被占用时自动往后找 |
| `-dir` | `~/Downloads/InternetSend` | 收到的文件保存位置 |
| `-pin` | 空（不需要口令） | 访问口令；`auto` 随机生成 6 位数字。**在公司/公共 Wi-Fi 上建议开启** |
| `-no-qr` | `false` | 不在终端打印二维码 |
| `-version` | | 显示版本 |

### 打包发布

```bash
make build          # 当前平台 → dist/internet-send
make release        # 交叉编译 6 个平台 → dist/
```

推送 `v*` 标签（如 `git tag v0.1.0 && git push origin v0.1.0`）会触发 GitHub Actions 自动构建并发布 Release。

## 工作原理

- 运行 `internet-send` 的机器是**主机（Hub）**：一个 Go HTTP 服务，内嵌网页界面。
- 每个浏览器是一个**设备**，通过 SSE（Server-Sent Events）长连接上线并接收实时事件；设备 ID 存在浏览器本地，刷新后身份不变。
- 发送文件 = 浏览器把文件流式上传到主机并落盘到 `-dir`；接收方从主机下载。
- 因此：**所有经过主机的文件都会保存在主机磁盘上**（主机本身就是一个"收件箱"）。设备之间的直连（WebRTC P2P）在路线图里。

```
internal/server   HTTP 接口、设备/消息中心（hub）、口令鉴权
internal/netutil  局域网 IP 识别（优先 Wi-Fi/热点网段，排除 Docker/VPN 虚拟网卡）
web/              网页界面（原生 JS，无构建步骤，go:embed 打包进程序）
main.go           命令行入口
```

### HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/info` | 主机信息、是否需要口令、局域网地址 |
| POST | `/api/login` | `{"pin": "..."}` 登录 |
| GET | `/api/events?device=&name=&platform=` | SSE：`hello` / `devices` / `item` 事件 |
| POST | `/api/upload?device=&to=&name=` | 请求体为文件原始字节 |
| POST | `/api/text` | `{"device","to","text"}` |
| GET | `/api/items/{id}/file?device=[&inline=1]` | 下载（支持 Range） |
| GET | `/api/qr?text=` | 生成二维码 PNG |

## 常见问题

**手机打不开地址？**
- 确认手机和电脑在**同一个** Wi-Fi / 热点（不是 4G/5G 流量）。
- Windows 首次运行会弹防火墙提示，请勾选「专用网络」并允许；如果当初点了拒绝，在「Windows 安全中心 → 防火墙 → 允许应用通过防火墙」里放行。
- macOS 首次运行如提示"是否允许接受传入网络连接"，选择允许。
- 部分路由器/公共 Wi-Fi 开启了 **AP 隔离（访客网络）**，设备之间互相访问不了；换用手机热点即可。
- 电脑开着 VPN/代理软件的 TUN 模式时，可能识别错网卡；终端会列出所有地址，可逐个尝试。

**用手机开热点、电脑连上，能用吗？** 能。电脑运行 Internet Send，手机打开终端里显示的地址即可（iPhone 热点地址一般是 `172.20.10.x`，安卓多为 `192.168.43.x`）。

**手机能当主机吗？** v0.1 主机需要电脑。手机端 App（可做主机、后台接收）在路线图中。

## 路线图

见 [docs/PRODUCT_PLAN.md](docs/PRODUCT_PLAN.md)。

## 开发

```bash
make test     # go vet + 单元/集成测试（含 race 检测）
make run      # 本地运行
```
