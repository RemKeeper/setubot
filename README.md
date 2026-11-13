# setubot

基于 ZeroBot 的 QQ 机器人项目，包含聊天 Agent、绘图、小红书图片流程、可选 Exa 搜索，以及可选 E-Hentai/ExHentai 查询工具。

## 功能概览

- Agent 对话：通过 OpenAI 兼容接口调用模型，并支持工具调用与本地 skill。
- QQ 接入：通过 OneBot WebSocket/HTTP 驱动连接机器人框架。
- 小红书图片流程：通过本地 Camoufox API 浏览、筛选、点赞、收藏、提取图片并发送。
- 绘图：可选调用 OpenAI 兼容图像接口。
- Exa 搜索：可选调用 Exa API 联网搜索。
- EH/EX 查询：可选使用本地 Cookie、代理、标签翻译和图片缓存下载工具。
- 高德出行：内置 `amap_route` / `amap_place` / `amap_info` / `amap_map_image` 四个原生工具，做路线规划与地区信息收集；`amap-mcp/` 另提供一份能力更全的独立 MCP 版本。

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
- `切换模型`：管理员命令。机器人会通过当前 `agent.baseURL` 的 `/v1/models` 接口获取模型列表，回复编号即可切换；也可以使用 `切换模型 <关键词>` 进行不区分大小写的模糊匹配。唯一匹配会直接切换，多项匹配会进入编号选择，配置会自动保存到 `config.json`。
- `agent.vision.enabled`：是否启用 QQ 消息及引用消息的图片识别。

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
- `maxContextChars`：发送给模型的消息字符预算，默认 `300000`；超出后优先丢弃较早历史，作为 token 上限前的安全闸门。
- `maxToolResultChars`：单条工具结果字符上限，默认 `60000`；避免大型 HTML、JSON 等进入模型上下文。
- `historyBootstrap`：当当前 AI 会话没有上下文时，从 OneBot 后端读取最近聊天消息补充语境。默认启用，`minCount`/`maxCount` 会限制在 `4` 到 `7` 条，`maxChars` 默认 `6000`。执行“重置上下文”或切换模型后，下一次对话不会读取历史消息，以避免旧上下文被重新注入。该功能需要 OneBot 后端支持群聊/私聊历史接口。
- `vision.enabled`：是否启用图片输入。开启后支持私聊图片、群聊 `@机器人 + 图片`，以及回复图文消息提问。
- `vision.mode`：识图模式。`direct` 会把图片直接发送给 `agent.model`；`tool` 不会把图片发送给主模型，而是提供 `analyze_images` 工具，由独立视觉模型识别后把文字结果返回主模型。主模型不支持视觉时应使用 `tool`。两种模式下图片链接都会先反转义（&amp; 还原为 &）；`tool` 模式会把可直接访问的原始链接写进图片占位符，`analyze_images` 的返回结果末尾同样会附上图片来源链接，方便反查图片出处或再次使用同一张图。
- `vision.baseURL`、`vision.apiKey`、`vision.model`：独立视觉工具使用的 OpenAI 兼容接口。留空时分别继承 `agent.baseURL`、`agent.apiKey`、`agent.model`。
- `vision.systemPrompt`：视觉模型的系统提示词。
- `vision.detail`：OpenAI 兼容视觉接口的图片细节级别，可选 `auto`、`low`、`high`，无效值按 `auto` 处理。
- `vision.maxImages`：单次识图最多传入的图片数量，默认 `8`；先取引用消息图片，再取当前消息图片，并自动去重。
- `vision.maxImageBytes`：单张远程图片下载大小上限，默认 `20971520`（20 MiB）。工具会由机器人本地下载 HTTP(S) 图片，校验类型后转换为 Base64 Data URL，再发送给视觉模型，避免模型侧访问 QQ 临时链接时触发防盗链。
- `vision.maxResultChars`：视觉模型结果返回主对话前的字符上限，默认 `12000`。
- `vision.temperature`：视觉模型温度，建议使用较低值以减少识别结果臆测。
- `taskGuard`：长流程任务约束配置；启用后，当用户请求命中关键词时会注入完成度约束提示，并可用 `maxSteps` 提高该轮工具循环上限。
- `forwardImage.webpSurfacePath`：合并转发隐藏图（`webpHidden=true`）使用的表层图片路径，作为第一帧短暂显示。输出尺寸由隐藏图片决定。
- `forwardImage.webpCacheMaxBytes`：隐藏图 WebP 缓存目录（`forward_image_webp_cache`）总大小上限，默认 `536870912`。
- `debug`：是否记录请求体到 `debugLogPath`。公开部署建议关闭。
- `debugTrace`：调试跟踪页面配置。`enabled` 未设置时跟随 `debug`；开启后会在 `addr`（默认 `0.0.0.0:18080`）启动内网 HTTP 页面，实时展示每轮请求的模型思考过程、模型请求/响应、工具调用参数与返回、以及组合消息发送到 QQ 的完整结构。`maxEvents` 控制内存中保留的事件条数（默认 2000），`maxBodyChars` 控制单条事件正文的字符上限（默认 20000，超出会截断）。页面无鉴权，只建议在可信内网开启，公开部署请保持关闭。
- `reasoning`：思考内容请求配置。很多 OpenAI 兼容网关默认不返回 `reasoning_content`，只统计 `reasoning_tokens`，此时跟踪页面只能看到「思考 tokens」而看不到思考文本。开启后会在 `/chat/completions` 请求体里注入 `include_reasoning=true`（`includeReasoning`，默认 true），可选注入 `reasoning_effort`（`effort`：`low`/`medium`/`high`），`extra` 可追加任意网关私有字段（如 `{"reasoning":{"enabled":true}}`）。已存在的同名字段不会被覆盖。同时会从原始响应体中补抓 `reasoning_content`、`reasoning`、`thinking`、`reasoning_details` 等字段，兼容非标准网关。
  - **注意**：注入参数只能让「支持输出思考」的模型把思考文本回传，无法让本身不输出思考的模型产生思考文本。实测同一网关下 `mistral/deepseek-v4-1-flash-1017` 无论注入什么参数都只返回 `reasoning_tokens` 而无 `reasoning_content`；`diai/deepseek-v4.1-flash-1017`、`command/deepseek/deepseek-v4.1-flash`、`dpsk/deepseek-flash`、`cpa/gemini-3.8-flash-high` 可以正常回传。跟踪页面的「思考提示」会区分「参数未注入」和「网关不回传」两种情况。

### `draw`

- `enabled`：是否启用绘图。
- `baseURL`、`apiKey`、`model`：OpenAI 兼容图像接口配置。
- `maxImages`、`defaultSize`、`timeout`：绘图数量、尺寸和超时。

### `browser`

小红书流程依赖 Camoufox API：

- `agent.browser.enabled`：是否允许 Agent 调用浏览器工具。
- `agent.browser.baseURL`：Camoufox API 地址，默认 `http://127.0.0.1:58000`。
- `agent.browser.maxResponseBytes`：浏览器 HTTP 响应读取上限，默认 `262144` 字节。
- `agent.browser.maxResultChars`：浏览器子代理内部单次结果和最终摘要的字符上限，默认 `40000`。
- `agent.browser.maxSubagentSteps`：一次浏览器子代理任务的最大步骤数，默认 `12`。

主 Agent 只暴露 `browser_task`。具体的 goto/click/type/evaluate 等操作由独立、短生命周期的浏览器子代理执行，中间 HTML 和工具消息不会保存到主对话历史；截图 base64 也不会注入模型上下文。

### `exa`

- `enabled`：是否启用 Exa 搜索。
- `apiKey`：Exa API Key。
- `defaultType`、`defaultNumResults`：默认搜索类型和数量。

### `ehTag` 和 `ehReq`

EH/EX 工具默认建议关闭，确认需要后再启用。

- `ehTag.enabled`：启用 EhTagTranslation 标签索引。
- `ehTag.blockedTags`：agent 不可见的标签屏蔽列表，命中后从所有返回给 agent 的标签中剔除（搜索结果、详情页标签、`eh_tag_search`/`eh_tag_resolve_keyword`/`eh_tag_translate` 均生效）。支持三种写法：`namespace:key` 精确匹配（如 `female:lolicon`）、`key` 匹配任意命名空间（如 `lolicon`）、`namespace:*` 屏蔽整个命名空间（如 `female:*`）。匹配不区分大小写，`_` 与空格等价。
- `ehReq.enabled`：启用 EH/EX 请求工具。
- `ehReq.cookie`：不建议直接写入真实 Cookie，优先使用环境变量或本地文件。
- `ehReq.cookieEnv`：Cookie 环境变量名，默认 `EHENTAI_COOKIE`。
- `ehReq.cookiePath`：Cookie 文件路径，默认 `.secrets/ehentai.cookies`。
- `ehReq.proxyURL`：EH/EX 专用代理地址，例如 `http://127.0.0.1:7890`。
- `ehReq.imageCacheMaxBytes`：EH 正文图片本地缓存总大小上限，默认 `2147483648`。图片按 `eh_image_cache/{gid}/{token}/` 两层目录缓存，超过总大小后优先清理最旧文件。
- `ehReq.galleryCooldownMs`：`eh_send_gallery` 相邻 EH 页面请求的最小间隔毫秒，默认 `1100`。
- `ehReq.galleryRetries`：`eh_send_gallery` 单个页面请求的最大尝试次数，默认 `3`。

### `eh_req_search` / `eh_req_gallery` 的 `format` 参数

两个请求工具都支持 `format`：

- `raw`（默认）：返回原始 HTML，放在 `body` 字段，受 `ehReq.maxBodyChars` 截断。
- `markdown`：返回解析后的结构化 markdown，放在 `markdown` 字段，推荐 agent 优先使用。

实测压缩效果：搜索页 25 条结果 66.7 KB → 13.3 KB（约 20%），详情页 17 页作品 26.2 KB → 1.4 KB（约 5.5%）。空结果、配额页（509）、登录失效都会转成一句明确提示。

解析逻辑对齐 JHenTai 的 `eh_spider_parser.dart`：按 `.itg.gltm/.gltc/.glte/.gld` 探测搜索页布局；评分按雪碧图坐标换算（`x` 每 `-16px` 减 1 星，`y=-21px` 为半星）；封面优先取 `data-src`；详情页标签按 `id="td_{namespace}:{key}"` 解析并按命名空间合并，`class="gtw"` 标注「(错误)」；详情页评论解析 `#cdiv > .c1`，输出作者、评分、时间与正文，最多 20 条、单条正文最多 500 字。

### `eh_send_gallery` 整本发送

`eh_send_gallery` 是 EH/EX 整本发送的一站式工具：只要传入作品链接（例如 `https://exhentai.org/g/4022117/22c0b08fdc/`），它就会自动完成后续全部步骤，agent 不需要再手工串联 `eh_req_gallery`、`eh_req_image_page`、`eh_download_images` 或 `send_forward_images_batches`。

- 自动翻页拉取作品正文缩略图入口，解析标题、总页数与每页图片页地址。
- 解析正文图片页并下载到本地 `eh_image_cache/{gid}/{token}/` 缓存，已缓存图片直接复用。
- 默认把每张图转成以 `agent.forwardImage.webpSurfacePath` 为表层的 WebP 隐藏图；`webpHidden=false` 时直接发原图。
- 按 `batch_size`（默认 30，上限 30）切片，每一片作为一份独立的合并转发聊天记录发送。
- 默认发送全本；传 `first_n=N` 时只发送画廊最前面的 N 张图（`first_n=0` 或不传即全本），此时缩略图翻页也会在凑够 N 张后提前停止。
- 拉取正文自带冷却（`cooldown_ms`）与重试（`retries`），命中 EH 配额限制时自动停止后续拉取。
- 全部批次处理完后一次性回传结构化结果：`ok`/`complete`、`batchTotal`/`batchSent`/`sentImages`、`batches[]`（每批的 `ok`、`count`、`firstPage`/`lastPage`、`messageId`、`attempts`、`elapsedMs`、`error`），以及 `failures[]` 里按页码列出的解析/下载失败明细。

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

- `POST /api/act`（统一动作接口：goto/click/click_text/fill/type/press/hover/select/scroll/wait/wait_selector/back/forward/reload）
- `GET /api/observe`（页面状态与可交互元素）
- `GET /api/markdown`（正文转 Markdown）
- `GET /api/html`
- `GET /api/screenshot`
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

## 调试跟踪页面

`agent.debugTrace.enabled=true`（或 `agent.debug=true` 且未显式关闭 `debugTrace`）时，进程启动后会同时监听 `agent.debugTrace.addr`（默认 `0.0.0.0:18080`），浏览器打开 `http://<内网IP>:18080/` 即可查看实时跟踪：

- **模型思考过程**：每轮模型响应里的 `reasoning_content` 与正文。若网关只返回 `reasoning_tokens` 而不回传思考文本，事件里会显示「思考提示」，此时按 `agent.reasoning` 配置注入 `include_reasoning` / `reasoning_effort` 即可拿到思考内容。
- **模型请求/响应**：请求消息列表、工具定义数量、温度、耗时、token 用量、结束原因。
- **工具调用**：工具名、调用 ID、完整参数 JSON、工具返回内容与耗时。
- **QQ 发送**：每次 `ctx.Send` 的消息类型（思考提示/最终回复/合并转发/小红书图片等）、目标会话、消息 ID、段类型统计与完整消息结构 JSON。
- **子流程**：浏览器子代理、`analyze_images` 视觉模型、上下文摘要与记忆抽取的模型调用同样会记录。

页面按「一轮请求」分组，支持按事件类型过滤、关键词搜索、折叠分组、清空事件。事件保存在内存环形缓冲中（`maxEvents`，默认 2000 条），单条正文超过 `maxBodyChars`（默认 20000 字符）会截断；base64 图片正文不会写入事件。页面无鉴权，只建议在可信内网开启。

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

## 高德出行工具（amap）

主 Agent 是 Go 的 OpenAI 兼容 function calling，**直接内置了 4 个高德工具**，不需要跑 MCP 服务，也不需要 Python。

### 配置

```json
"amap": {
  "enabled": true,
  "apiKey": "",
  "apiKeyEnv": "AMAP_API_KEY",
  "baseURL": "https://restapi.amap.com",
  "timeout": 10,
  "cacheTTL": 120
}
```

`apiKey` 留空时会读环境变量 `AMAP_API_KEY`（推荐，避免把 Key 写进 `config.json`）：

```bash
export AMAP_API_KEY=你的Web服务Key
```

> `restapi.amap.com` 只接受**「Web服务」**类型的 Key。若返回 `infocode=10009 / USERKEY_PLAT_NOMATCH`，到 <https://console.amap.com/dev/key/app> 新建一个服务平台为「Web服务」的 Key 再替换即可。工具会把错误码翻译成中文可执行建议。

### 工具清单

`agent.amap.enabled=true` 时自动注册，共 4 个：

| 工具 | 能力 | 关键参数 |
| --- | --- | --- |
| `amap_route` | 驾车 / 步行 / 骑行 / 公交地铁路线规划 | `mode`, `origin`, `destination`, `city`, `strategy`, `waypoints`, `depart_time`, `detail` |
| `amap_place` | 地理编码、逆地理编码（含周边 POI/AOI）、POI 检索与详情、输入提示 | `action`（geocode/regeo/poi/poi_detail/tips）, `keywords`, `city`, `location`, `radius` |
| `amap_info` | 天气（实时+预报）、行政区划 adcode、实时路况 | `action`（weather/district/traffic）, `city`, `road`/`rectangle`/`location` |
| `amap_map_image` | 生成静态地图图片 URL（可带标记点/折线） | `location`, `zoom`, `markers`, `paths` |

### 设计要点

- **地名即坐标**：`origin`/`destination` 传「杭州西湖文化广场」或 `"120.16,30.28"` 都行，内部自动地理编码，纯坐标不额外发请求。
- **歧义自查**：地名命中多个候选、或与 `city` 提示不符时，输出末尾附 `⚠` 提示并给出命中地址与候选数。真实踩过的坑：不带 `city` 时「杭州西湖文化广场」会被解析到**江西省南昌市西湖区**，驾车变成 531 km。
- **路况退化**：`traffic/status/road` 只覆盖主要道路，查不到（`20003`）时自动退化为「定位该路 → 查 2km 圆形区域」，并在输出里说明。
- **Token 友好**：默认 `detail=summary` 只给摘要；`steps` 模式最多渲染 12 步，超出提示省略；POI 最多 10 条。
- **缓存**：相同参数的 GET 结果缓存 `cacheTTL` 秒（默认 120），agent 反复试错时不浪费配额。
- **失败可读**：所有错误返回 `错误：... | infocode=... | 建议：...`，模型能自己纠正参数。

### 验证

```bash
# 单元测试（httptest 模拟高德，不依赖外网）
go test ./internal/plugins/agent/ -run TestAmap

# 真实接口冒烟（需要 Key）
AMAP_LIVE=1 AMAP_API_KEY=你的Key go test ./internal/plugins/agent/ -run TestAmapLive -v
```

### 可选：独立 MCP 版本

`amap-mcp/` 目录下另有一份 Python 实现的 MCP 服务，能力更全（15 个工具，含批量测距、坐标转换、IP 定位、`raw_request` 逃生舱），适合挂到 Claude Desktop / Cursor / DSH 等支持 MCP 的客户端。主 Agent 不需要它，两者互不依赖；字段命名与设计约定保持一致。详见 [`amap-mcp/README.md`](amap-mcp/README.md)。


