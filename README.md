# setubot

基于 ZeroBot 的 QQ 机器人项目，包含聊天 Agent、绘图、小红书图片流程、可选 Exa 搜索，以及可选 E-Hentai/ExHentai 查询工具。

## 功能概览

- Agent 对话：通过 OpenAI 兼容接口调用模型，并支持工具调用与本地 skill。
- QQ 接入：通过 OneBot WebSocket/HTTP 驱动连接机器人框架。
- 小红书图片流程：通过本地 Camoufox API 浏览、筛选、点赞、收藏、提取图片并发送。
- 绘图：可选调用 OpenAI 兼容图像接口。
- Exa 搜索：可选调用 Exa API 联网搜索。
- EH/EX 查询：可选使用本地 Cookie、代理、标签翻译和图片缓存下载工具。

## 环境要求

- Linux/macOS 或可运行 Go 的环境。
- Go 版本以 `go.mod` 为准。
- Python 3，用于运行 `skills/xhs_setu.py` 和 `skills/xhs_dislike.py`。
- OneBot 实现，例如 NapCat、Lagrange.OneBot、go-cqhttp 派生实现等。
- 可选：Camoufox API 服务，默认地址 `http://127.0.0.1:58000`。

## 快速开始

1. 安装依赖

```bash
go mod download
pip install -r requirements.txt
```

2. 创建配置文件

```bash
cp config.example.json config.json
```

3. 编辑 `config.json`

重点修改：

- `nickName`：机器人昵称，群聊中需要被 @ 或昵称触发。
- `superUsers`：管理员 QQ 号列表。
- `drivers[].url`：OneBot WebSocket 或 HTTP 地址。
- `drivers[].accessToken`：OneBot access token；没有配置鉴权时可留空。
- `agent.apiKey`：OpenAI 兼容接口密钥。
- `agent.baseURL`：OpenAI 兼容接口地址，例如 `https://api.openai.com` 或本地代理地址。
- `agent.model`：聊天模型名。

4. 启动 OneBot 端

以 WebSocket Client 模式为例，`config.json` 中：

```json
{
  "type": "ws-client",
  "url": "ws://127.0.0.1:11451",
  "accessToken": "你的 OneBot token"
}
```

确保 OneBot 服务端监听地址、端口、token 与这里一致。

5. 启动机器人

```bash
go run .
```

或构建后运行：

```bash
go build -o setubot .
./setubot
```

## 配置说明

### 顶层配置

- `nickName`：机器人昵称数组。
- `commandPrefix`：命令前缀，当前插件主要通过私聊文本或群聊 @ 触发。
- `superUsers`：管理员 QQ 号数组。
- `drivers`：ZeroBot 驱动配置，支持 `ws-client`、`ws-server`、`http`。

### `agent`

- `enabled`：是否启用 Agent。
- `baseURL`：OpenAI 兼容接口根地址；代码会自动补 `/v1`。
- `apiKey`：模型接口密钥。
- `model`：聊天模型名。
- `systemPrompt`：默认系统提示词。
- `skillDir`：skill 目录，默认 `skills`。
- `memoryDir`：记忆目录，默认 `data/memory`。
- `maxToolRounds`：单轮最多工具调用轮数。
- `debug`：是否记录请求体到 `debugLogPath`。公开部署建议关闭。

### `draw`

- `enabled`：是否启用绘图。
- `baseURL`、`apiKey`、`model`：OpenAI 兼容图像接口配置。
- `maxImages`、`defaultSize`、`timeout`：绘图数量、尺寸和超时。

### `browser`

小红书流程依赖 Camoufox API：

- `agent.browser.enabled`：是否允许 Agent 调用浏览器工具。
- `agent.browser.baseURL`：Camoufox API 地址，默认 `http://127.0.0.1:58000`。

### `exa`

- `enabled`：是否启用 Exa 搜索。
- `apiKey`：Exa API Key。
- `defaultType`、`defaultNumResults`：默认搜索类型和数量。

### `ehTag` 和 `ehReq`

EH/EX 工具默认建议关闭，确认需要后再启用。

- `ehTag.enabled`：启用 EhTagTranslation 标签索引。
- `ehReq.enabled`：启用 EH/EX 请求工具。
- `ehReq.cookie`：不建议直接写入真实 Cookie，优先使用环境变量或本地文件。
- `ehReq.cookieEnv`：Cookie 环境变量名，默认 `EHENTAI_COOKIE`。
- `ehReq.cookiePath`：Cookie 文件路径，默认 `.secrets/ehentai.cookies`。
- `ehReq.proxyURL`：EH/EX 专用代理地址，例如 `http://127.0.0.1:7890`。

## 小红书偏好修改

小红书筛选偏好主要在 `skills/xhs_setu.py` 顶部维护。

### 推荐修改位置

- `PREFER_KEYWORDS`：普通偏好关键词，命中后提高候选评分。
- `HIGH_WEIGHT_KEYWORDS`：高权重偏好关键词，命中后显著提高评分。
- `NEGATIVE_KEYWORDS`：负面关键词，命中后直接排除。

示例：

```python
PREFER_KEYWORDS = [
    "cos", "二次元", "你的普通偏好",
]

HIGH_WEIGHT_KEYWORDS = [
    "你的强偏好",
]

NEGATIVE_KEYWORDS = [
    "广告", "推广", "你不想看到的内容",
]
```

### 修改建议

- 想更常看到某类内容：加入 `PREFER_KEYWORDS`。
- 明确非常喜欢某类内容：加入 `HIGH_WEIGHT_KEYWORDS`。
- 想完全过滤某类内容：加入 `NEGATIVE_KEYWORDS`。
- 负面词要谨慎，过宽会导致大量正常帖子被过滤。
- 如果只是临时搜索，优先让用户说“来点 XX 图”，不要直接改脚本。

### 不喜欢工作流

`skills/xhs_dislike.py` 用于撤销当前帖子的点赞和收藏，并可把关键词加入负面列表。

- 用户只说“不喜欢”：调用 `xhs_dislike`，不传关键词。
- 用户明确说“以后别推 XX”：调用 `xhs_dislike` 并传入 `keyword`。
- 脚本会尝试写入 `data/memory/xhs.md`，并可更新 `skills/xhs_setu.py` 的 `NEGATIVE_KEYWORDS`。

注意：小红书帖子链接的 `xsec_token` 有时效性，不喜欢操作需要尽快执行。

## Camoufox API

`xhs_setu.py` 和 `xhs_dislike.py` 默认访问：

```text
http://127.0.0.1:58000
```

需要提供这些端点：

- `POST /api/goto`
- `POST /api/click`
- `POST /api/scroll`
- `GET /api/html`
- `POST /api/evaluate`

启动 bot 前建议先确认浏览器服务可访问，并已登录小红书账号。

## 常用命令

运行测试：

```bash
go test ./...
```

格式化 Go 代码：

```bash
gofmt -w main.go internal
```

运行小红书脚本：

```bash
python3 skills/xhs_setu.py --count 1 --scroll 2
python3 skills/xhs_setu.py --keyword "关键词" --count 1 --scroll 3
python3 skills/xhs_dislike.py --keyword "不喜欢的关键词"
```

## 敏感文件与开源注意事项

不要提交以下文件或目录：

- `config.json`：包含真实 API Key、OneBot token、QQ 号、Cookie 等。
- `.secrets/`：本地 Cookie 或其它密钥。
- `data/`：运行日志、记忆、缓存、图片缓存。
- `setubot`：本地构建产物。

当前 `.gitignore` 已包含这些路径。开源时建议只提交：

- `config.example.json`
- `README.md`
- 源码、skill、依赖文件

## 故障排查

### 读取配置失败

确认当前目录存在 `config.json`，并且 JSON 格式合法。

### Agent 提示 API Key 未配置

检查 `agent.apiKey`，或确认你的 OpenAI 兼容接口是否要求鉴权。

### 群聊里机器人不响应

群聊中默认需要 @ 机器人或使用机器人昵称触发。检查 `nickName` 和 OneBot 事件是否正常上报。

### 小红书脚本失败

检查：

- Camoufox API 是否启动。
- `agent.browser.baseURL` 是否正确。
- 浏览器是否已登录小红书。
- 页面 DOM 是否变化，可参考 `skills/xhs-dom-and-url-patterns.md`。

### EH/EX 图片发送失败

检查：

- `agent.ehReq.enabled` 是否开启。
- Cookie 是否通过环境变量或 `.secrets/ehentai.cookies` 配置。
- 代理是否可用。
- 图片缓存路径是否是绝对路径返回。
