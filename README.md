# DeepAICode2API

[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue)](LICENSE)

DeepAI Code 的 Go 网关，提供 OpenAI 和 Anthropic 兼容接口、账号池以及管理控制台。它可以导入现有账号，也可以使用 Hotmail / Outlook 邮箱通过 Microsoft Graph 完成注册和邮件验证。

## 功能

- 提供 OpenAI 兼容的 Chat Completions、Responses、Models 和音频转写接口，以及 Anthropic Messages 接口。
- 按分组管理账号和网关密钥，支持配额查询、轮询、故障切换和请求日志。
- 通过控制台导入 Graph 邮箱、批量注册并查看任务进度和日志；已注册及结果待确认的邮箱会自动跳过。
- 使用独立管理登录页、管理密码和同源写入检查；可使用 AES-256-GCM 加密网关状态文件。
- 单个 Go 可执行文件，控制台资源内嵌；支持 Docker Compose。

## 快速开始

需要 Go 1.24 或 Docker Compose。复制配置示例后，设置管理密码与状态文件加密主密钥：

```bash
cp .env.example .env
# 编辑 .env，设置 DEEPAI2API_ADMIN_PASSWORD 和 DEEPAI2API_MASTER_KEY
# 例如可用 openssl rand -hex 32 生成 32 字节的十六进制主密钥
```

直接使用 GitHub 自动发布的镜像（无需在服务器编译）：

```bash
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d
```

镜像为 `ghcr.io/hl320217/deepaicode2api:latest-browser`，支持 Linux AMD64 和 ARM64。浏览器版内置 Chromium 和 Xvfb，默认在虚拟显示器中运行有头浏览器，服务器无需安装桌面；可在 `.env` 设置 `DEEPAI_GOOGLE_HEADLESS=true` 切换无头模式。Xvfb 不包含远程桌面；验证码或其他需要人工操作的页面仍需额外的交互环境。仅使用已授权 Key 调用 API 时，可设置 `DEEPAI2API_IMAGE=ghcr.io/hl320217/deepaicode2api:latest` 使用较小的 API 镜像。

`docker-compose.ghcr.yml` 用命名卷保存 `/data`，容器更新后账号和设置仍保留。更新镜像时重新执行上面的两条命令。已有账号不会随源码或镜像上传；迁移现有账号时需要单独迁移状态文件及其加密主密钥。

首次发布的 GHCR 包可能默认为私有；可在 GitHub 仓库的 Packages 页面将包设为 Public，或先用有 `read:packages` 权限的账号执行 `docker login ghcr.io`。仓库和镜像的可见性需要分别检查。

也可在服务器从源码构建 API 镜像：

```bash
docker compose up -d --build
```

打开 [http://127.0.0.1:10087/](http://127.0.0.1:10087/)，输入配置的管理密码后进入控制台。Compose 默认只监听本机；如需远程使用，请通过 HTTPS 反向代理或 SSH 隧道访问。

也可直接编译：

```bash
go build -o bin/deepaicode2api .
./bin/deepaicode2api
```

管理页面中可以导入本机桌面端账号、添加上游 API Key、创建网关 API 密钥，以及管理注册任务。部分机器或容器环境需要设置 `DEEPAI_MACHINE_ID`；其他配置见 [.env.example](.env.example)。

## 控制台资源管理

- **账号池**：搜索、筛选与排序，点击账号打开详情抽屉修改名称和分组；勾选当前页账号后，可批量刷新额度、修改分组、启停或删除。选择范围和数量会明确显示。余额与验证状态由实际检查更新。
- **邮箱资产**：Graph 邮箱与 Google 账号分开维护；支持单条添加、凭据更新、批量删除。文件导入先预览新增和更新数量，再确认保存。删除登录资料不会删除已入池的 DeepAI 账号，历史状态仍保留；任务正在使用号池时禁止修改资料。
- **任务中心**：集中创建注册、Google 授权任务及查看日志。Google 支持对所选账号授权，已授权账号会跳过；注册历史保留当前进程内的近期任务，进程重启后不恢复。
- **API 密钥**：支持搜索、修改备注与授权分组、启停和撤销。列表默认遮挡密钥；管理员可在密钥详情中再次查看、隐藏或复制。账号池详情中的上游 Key 同样支持这些操作。完整值仅在点击操作后单独请求，不随列表返回，也不写入浏览器持久存储。
- **操作反馈**：删除需要确认，批量结果显示成功与失败项目；保存失败返回明确错误。设置草稿不会被列表刷新覆盖，保存期间锁定字段，避免重复提交与覆盖新输入。

控制台仍随 Go 可执行文件内嵌，包含浅色、深色和窄屏布局，无需 Node 或前端构建服务。

## Hotmail 邮箱注册

在控制台的「邮箱资产 → Graph 邮箱」页面导入 UTF-8 文本文件，每行一个邮箱：

```text
邮箱地址----邮箱密码----client_id----refresh_token
```

Graph 使用 `client_id` 和 `refresh_token` 授权；令牌需要具有 Microsoft Graph 委托 `Mail.Read` 和 `offline_access` 权限。多次导入会按邮箱地址合并；重复地址使用最新导入的凭据，已有邮箱状态保留。文件导入后，可在「任务中心」设置注册数量（最多 1,000）、并发数（最多 10）和代理池。

启用邮件验证时，注册前会检查 Graph 读信权限。只有 Outlook IMAP/POP 权限的令牌不能用于 Graph；授权检查失败时不会提交注册，邮箱会退回可用状态，需重新授权并导入凭据。

DeepAI 网站账号注册成功不代表获得 DeepAI Code 使用资格。若 Code 接口返回 `social_login_required`，任务会保留为 `registered`，验证阶段显示 `blocked`，并提示通过 Google 或 Apple 登录。账号凭据保存在本地台账，但不会入池或导出为可用密钥。可通过控制台「账号池 → 添加账号 → 浏览器登录」导入符合上游条件的账号。

邮箱状态分为「可用」「已注册」和「待确认」。注册请求发生网络中断等无法确定结果的情况时，邮箱会标为待确认，避免自动重复注册。邮箱分配状态会持久保存，重新导入时保留原有状态。不同服务实例的状态不会自动同步，不应让多个实例同时使用同一份邮箱池。

命令行工具通过 `DEEPAI_GRAPH_MAIL_FILE` 读取同样格式的邮箱文件：

```bash
go build -o bin/register ./cmd/register
./bin/register -doctor
./bin/register -count 3 -concurrency 2
```

## Google 号池自动授权

在「邮箱资产 → Google 账号」导入 UTF-8 文本文件，每行一个 Google 账号：

```text
user@gmail.com----Google密码
user@company.com----Google密码
student@school.edu----Google密码
```

也支持制表符或 `|` 分隔。企业和教育邮箱必须确实是 Google 账号，且管理员允许 DeepAI 授权。重复导入只更新密码，保留授权结果。已授权账号自动跳过；失败或跳过的账号需勾选「重试失败或跳过的账号」。

在「任务中心」点击「开始自动授权」后，程序逐个打开独立浏览器，填写 Google 邮箱和密码，通过 DeepAI 自己的 Google 授权页面完成登录，接收桌面端回调并换取 API Key。只有 `/deepai_code/theos` 检查成功的账号才加入 DeepAI 账号池；检查通过不保证一定有免费余额。Google 密码不会复制到 DeepAI 账号的密码字段，也不会回传到控制台或写入授权日志。

需要在**运行网关的电脑或服务器**上安装 Chrome / Chromium / Edge，无需 Node 或 Python。可用 `DEEPAI_GOOGLE_BROWSER` 指定浏览器路径，`DEEPAI_GOOGLE_PROXY` 指定浏览器代理（不含代理账密）；API 网络仍使用原有代理配置。

`DEEPAI_GOOGLE_HEADLESS=auto`（默认）在没有 `DISPLAY` / `WAYLAND_DISPLAY` 的 Linux 上自动使用无头浏览器；Windows、macOS 和 Linux 桌面默认显示独立窗口。也可设为 `true` 强制无头，或 `false` 强制显示。无头模式仍使用真实浏览器执行 Google 网页授权，不是邮箱密码的纯 HTTP 协议登录。授权拿到 DeepAI Key 后，网关的 API 调用不再需要浏览器。

例如在 Debian / Ubuntu 安装 Chromium 后运行（可执行文件路径按实际安装调整）：

```bash
export DEEPAI_GOOGLE_BROWSER=/usr/bin/chromium
export DEEPAI_GOOGLE_HEADLESS=true
./bin/deepaicode2api
```

默认 Docker 构建只含 API 服务；需要从源码构建包含 Chromium/Xvfb 的浏览器镜像时：

```bash
docker compose -f docker-compose.yml -f docker-compose.browser.yml up -d --build
```

浏览器镜像以非 root 用户运行并保留 Chromium 沙箱；宿主内核和容器运行时需要允许沙箱创建所需的用户命名空间。默认 `DEEPAI_GOOGLE_HEADLESS=false` 会自动启动 Xvfb；设为 `true` 时直接运行无头 Chromium。Windows 有头授权已实际验证，Linux 下的 Google 登录成功率仍需在部署环境验证。

Workspace 首次登录告知页的「我了解」会自动确认；已识别的通行密钥、恢复手机号可选设置页会点击「以后再说」，继续 DeepAI 授权。程序不会创建通行密钥、设置恢复资料或修改 Google 密码；未知设置页留待人工处理。

验证码、双重验证、企业 SSO、企业管理员限制或 Google 的自动浏览器限制不能靠账密保证自动通过。出现这些情况，界面显示「需要处理」，在弹出的浏览器完成确认后程序自动继续；也可跳过或停止任务。默认连续 60 秒没有新的授权进展时自动关闭当前登录浏览器、跳过账号并处理下一个；反复显示相同进度、页面错误或人工确认提示不会延长等待。自动提交邮箱、密码或推进新的设置/授权步骤会重新计时。自动跳过计入任务的「跳过」数量，账号保留为失败状态并说明超时原因，可勾选「重试失败或跳过的账号」再次授权。

可通过 `DEEPAI_GOOGLE_IDLE_TIMEOUT=60s` 调整等待时间（例如 `30s`、`2m`，必须大于 0 且不超过 `10m`）。每个账号仍有 10 分钟总时限；该无进展计时仅用于浏览器登录，不限制后续的额度检查和入池操作。停止任务后未完成的账号退回可用；进程重启时未完成任务标为失败，需明确重试。

若出现 Google 的「此浏览器或应用可能不安全」或 `/signin/rejected` 拒绝页，程序会保留明确失败原因、结束当前账号并继续队列，不点击「重试」或等待完整超时。拒绝原因不会混同为密码错误。此处理能避免任务挂起，不能让 Google 接受已被拒绝的浏览器。

Google [官方说明](https://support.google.com/accounts/answer/7675428?hl=zh-Hans)明确列出自动化控制的浏览器可能无法登录。Google 的公开 OAuth / OpenID Connect 授权通过用户授权页完成身份验证，不提供本项目可使用的邮箱密码直换 DeepAI 授权接口；任意 Google access token 也不能当作 DeepAI Key。无头浏览器同样需要通过 Google 的登录检查。

需要人工授权时，可在运行网关的电脑上使用控制台「账号池 → 添加账号 → 浏览器登录」，在受支持的普通浏览器完成 DeepAI 自己的 Google 授权。该入口接收同机回调并使用 HTTP 换取 DeepAI Key。若希望避免常用浏览器保存登录会话，可由用户在单独的临时配置或无痕窗口打开入口。部署 Linux 服务也可先在本地完成授权，再导入已取得的 DeepAI Key；模型调用、额度查询和密钥交换本身均为 HTTP，不需要每次重新登录 Google。

无头模式识别到明确的验证码、双重验证、登录拒绝或必填设置时，会结束该账号并提示人工处理。外部企业 SSO 页面会留出短暂跳转时间，仍停留时也会结束并提示人工处理；无法识别的 Google 页面仍可能等待到登录超时。可在有界面的环境完成授权再导入 DeepAI Key，或在服务器配置 Xvfb/VNC 并以 `DEEPAI_GOOGLE_HEADLESS=false` 重试。无头不能保证通过 Google 风控。

Google 入池在首次查询 Theos 前保存账号的固定设备标识，重试、重新导入和同邮箱换 Key 都沿用已有值。授权成功只表示 Key 有资格访问 Code；可用额度必须以实时 `balance` 为准，不能按账号数量推算，也不保证上游一直沿用当前发放规则。

Google 授权入池时若尚无可用额度，会与邮箱注册共用延迟复查：依次等待 3、6、12 分钟，约在入池后的第 3、9、21 分钟再次查询，最多 3 次；查询到正额度或账号已删除就停止。复查沿用已保存的 Key 和设备标识，将上游实际返回的余额写入账号缓存和状态文件；上游仍返回 0 时不会填充额度。这是当前进程内的短期复查，重启后不会恢复，也不是每天自动刷新任务。Google 号池列表重新加载时读取关联 DeepAI 账号的最新额度，手动刷新额度或后台复查后不再固定显示授权时的旧值；授权日志仍保留当时结果。

Google 号池账密随网关状态文件保存，使用现有 `DEEPAI2API_MASTER_KEY` 时整体加密；未配置时按现有机制以明文状态文件保存。原始导入文件不会被复制保存。

## API

| 接口 | 用途 |
| --- | --- |
| `GET /v1/models` | 查询模型目录 |
| `POST /v1/chat/completions` | OpenAI 兼容对话 |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/messages` | Anthropic 兼容消息 |
| `POST /v1/messages/count_tokens` | 估算 token 数 |
| `POST /v1/audio/transcriptions` | 音频转写 |
| `POST /v1/deepai/web_search` | 联网搜索 |
| `GET /health` | 服务健康信息 |
| `/admin/api/*` | 受管理密码保护的管理接口 |

客户端请求网关接口时，按需使用控制台创建的 API 密钥：

```bash
curl http://127.0.0.1:10087/v1/models \
  -H 'Authorization: Bearer <网关API密钥>'
```

模型目录由上游提供；可用模型、配额和接口行为可能随上游变化。

### 动态代理与出口刷新

控制台「系统设置 → 动态代理」支持统一配置代理地址和供应商的 **GET 刷新 IP 链接**。启用后，控制台邮箱注册、Google 授权后的额度查询、手动/批量额度刷新和后台复查使用该代理。未启用时保留原有账号代理、注册代理池和环境代理行为。Google 浏览器登录本身仍使用原来的浏览器代理配置。

1. 填入完整代理 URL（`http://user:password@host:port`、HTTPS 或 SOCKS5）及供应商刷新链接。
2. 设置发放阈值（默认 5，可调整）和出口确认超时（默认 30 秒），保存后点击「检测出口与连接」。**保存配置不会调用刷新链接。**
3. 按需启用自动切换，或点击「立即刷新 IP」。刷新接口从服务端直连发起，不携带 DeepAI Key、不跟随重定向；随后通过配置的代理确认 IP 改变，并验证上游模型目录可访问。检测失败、出口不变、返回已用满的出口时暂停，不自动重复调用供应商刷新链接。
4. 「暂停」立即阻止新的注册/额度工作，已经发出的请求收尾；排查线路后点击「恢复」。同一动态代理的注册按账号串行，切换会等待受管理请求的响应体（包括流式响应）关闭。

计数以本项目观察到的新增正额度为准，不按注册数量或 HTTP 200 次数计数。查询前先不带 Machine 检查已有余额；已有正余额、重复查询、明确的零余额不会增加成功计数。发放记录按 API Key 摘要、出口和上游 `refresh_at` 持久化，重启不会清空；缺少有效刷新时间时使用 24 小时的保守本地期限。请求结果不明确时保留「待核对」记录并暂停，该记录占用暂定容量但不表示已发放；恢复后可通过原账号查询核对。

达到阈值后，在下一次需要领取额度的请求或新注册任务开始前切换。阈值是可配置的经验值，并非官方保证；其他程序的请求、共享出口和代理分流都可能使本地计数不完整。返回 0 本身不会触发无限换 IP。请使用稳定出口的代理；自动逐请求轮换的线路不适合此计数方式。

每个账号已有 Machine 保持不变，Instance 保持进程级固定。代理认证信息与刷新链接保存在服务端状态文件，沿用 `DEEPAI2API_MASTER_KEY` 的加密设置；管理 API 不回显完整 URL，表单留空保留已有值，浏览器不持久存储这些凭据。动态代理认证信息不会写入注册台账和交付物。此面板配置仅作用于网关进程内的任务，独立运行的注册 CLI 仍使用其原有代理参数。

## 数据与凭据

`.env`、`data/`、注册台账和交付物均已加入 `.gitignore`。不要将管理密码、上游 API Key、邮箱文件或代理凭据提交到仓库。

`DEEPAI2API_MASTER_KEY` 加密网关状态文件，但导入的 Graph 邮箱文件和注册凭据台账仍以本地文件形式保存，包含敏感信息。请限制文件和备份的访问权限，并分别保管加密主密钥。

## 开发与测试

推送 `main`、推送 `v*` 标签或手动触发 `Docker images` 工作流，会先运行 Go 检查，再发布 API 镜像和浏览器镜像到当前仓库所属账号的 GHCR。`main` 对应 `latest` / `latest-browser`；例如 `v1.0.0` 标签对应 `v1.0.0` / `v1.0.0-browser`；每次构建还会保留 `sha-<短提交号>` / `sha-<短提交号>-browser` 标签。工作流使用 GitHub 自动提供的 `GITHUB_TOKEN`，无需另配镜像仓库密码；仓库或组织需允许 Actions 写入 Packages。

```bash
go test ./...
go test -race ./internal/register ./internal/server
```

原项目来源：[2951461586/DeepAICode2API](https://github.com/2951461586/DeepAICode2API)。本仓库在原项目基础上增加 Graph 邮箱注册、邮箱状态管理和控制台改进。

## 许可证

[MIT](LICENSE)
