---
name: jhentai-gallery-query
description: "Use when: querying E-Hentai/ExHentai galleries by keyword or translated tag, fetching gallery metadata/details, resolving thumbnail hrefs, returning manga正文 image/page URLs, and guiding local Cookie setup. Triggers: JHenTai, E-Hentai, ExHentai, 漫画查询, 关键词漫画, 标签中文映射, 正文图片, gallery images, Cookie配置."
argument-hint: "关键词或标签；可选：站点 EH/EX、结果数量、正文图片数量"
---

# JHenTai Gallery Query

## 目标

根据用户输入的关键词、英文标签或中文标签，完成 E-Hentai/ExHentai 漫画查询流程，并返回漫画详情与正文图片地址。该 skill 是独立工作流，不依赖用户了解具体项目源码。

## 适用场景

- 用户要求查询指定关键词漫画、按标签搜索漫画、获取漫画详情或返回正文图片地址。
- 用户需要从中文标签映射到 E-Hentai 搜索表达式，例如 `中文` → `language:chinese`。
- 用户需要配置 EH/EX Cookie，用于访问登录态、ExHentai 或受限内容。

## 安全原则

- 不要要求用户把 Cookie、账号、密码、令牌直接发到聊天消息中。
- 需要 Cookie 时，引导用户在本地终端、`.env`、系统凭据管理器或本机配置文件中输入。
- 不要在回答、日志、错误信息、调试输出中回显完整 Cookie。
- 不要提交 Cookie 文件到 Git；若使用文件保存，必须加入 `.gitignore`。
- 仅保存必要 Cookie 字段，优先保存 `ipb_member_id`、`ipb_pass_hash`、`igneous`、`sk`。

## 用户输入参数

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `keyword` | 是 | 无 | 搜索关键词、中文标签、英文标签或高级搜索表达式 |
| `site` | 否 | `EX` 优先，失败回退 `EH` | `EH` 使用 `https://e-hentai.org`，`EX` 使用 `https://exhentai.org` |
| `limit` | 否 | `5` | 返回漫画结果数量 |
| `page` | 否 | `0` | 搜索结果页码，E-Hentai 从 `0` 开始 |
| `includeImages` | 否 | `true` | 是否解析正文图片 URL |
| `imageLimit` | 否 | `3` | 每个漫画最多解析多少张正文图片 |
| `preferOriginal` | 否 | `false` | 是否优先返回原图地址 |
| `useTagTranslation` | 否 | `true` | 是否尝试中文标签映射 |
| `cookiesPath` | 否 | `.secrets/ehentai.cookies` | 本地 Cookie 存储路径 |

## Cookie 配置流程

### Agent 本地请求工具

当前项目提供 `ehReqClient` 系列工具，agent 执行本 skill 时应优先通过这些工具请求 E-Hentai/ExHentai，不要自行拼接 Cookie，也不要向用户索要 Cookie 明文：

- `eh_req_search`: 请求搜索页 `{siteBase}/`，支持 `keyword`、`page`、`advanced` 和额外 query 参数。
- `eh_req_gallery`: 请求详情页 `/g/{gid}/{token}/`，支持缩略图分页 `page` 与 `show_comments`。
- `eh_req_api`: 请求官方 API `/api.php`，支持 `gdata`、`tagsuggest` 等 JSON payload。
- `eh_req_image_page`: 请求正文图片页 `/s/{imageHash}/{gid}-{pageNo}`，支持 `reload_key`。
- `eh_download_images`: 并发下载多个正文图片直链到本地缓存，全部成功后返回 `file:///` 本地图片地址，供 `send_forward_images` 发送；单图失败会自动重试，缓存最多保留 100 张并自动清理。

这些工具会从本地配置读取 Cookie 并注入请求头，只返回 `cookieLoaded` 布尔值，不会把 Cookie 内容返回给 agent。Cookie 来源按优先级读取：

1. `config.json` 的 `agent.ehReq.cookie`
2. 环境变量 `agent.ehReq.cookieEnv`，默认 `EHENTAI_COOKIE`
3. 本地文件 `agent.ehReq.cookiePath`，默认 `.secrets/ehentai.cookies`

EH/EX 请求可单独配置代理，不影响 OpenAI、Exa、浏览器等其它请求。代理来源按优先级读取：

1. `config.json` 的 `agent.ehReq.proxyURL`
2. 环境变量 `agent.ehReq.proxyEnv`，默认 `EHENTAI_PROXY`

代理地址应使用完整 URL，例如 `http://127.0.0.1:25621`。工具返回中会包含 `proxyEnabled` 布尔值，表示本次 EH 请求是否实际启用了代理。

工具只允许请求 `https://e-hentai.org`、`https://exhentai.org` 和 `https://api.e-hentai.org`，避免被用于访问无关域名。

推荐流程：

1. 搜索漫画列表时调用 `eh_req_search`。
2. 需要元数据或标签建议时调用 `eh_req_api`。
3. 获取漫画详情与缩略图入口时调用 `eh_req_gallery`。
4. 解析正文图片 URL 时调用 `eh_req_image_page`。
5. 需要发送正文图片时先调用 `eh_download_images`，再把返回的 `fileUrl` 列表传给 `send_forward_images`。
6. 若结果中 `cookieLoaded=false` 且访问 EX/受限内容失败，再引导用户在本地配置 Cookie。

1. 判断是否需要 Cookie
   - 查询 `EX`、访问 ExHentai、获取受限内容或图片页返回登录/权限错误时，需要 Cookie。
   - 普通 `EH` 搜索可尝试匿名访问，但结果可能受限。

2. 引导用户获取 Cookie
   - 让用户在浏览器登录 E-Hentai/ExHentai。
   - 打开开发者工具，进入 Application/Storage → Cookies。
   - 复制必要字段：`ipb_member_id`、`ipb_pass_hash`、`igneous`，可选 `sk`。
   - 明确提示用户不要把 Cookie 粘贴到聊天中。

3. 引导用户本地保存 Cookie
   - 推荐保存到工作区外的用户目录，或工作区内 `.secrets/ehentai.cookies`。
   - 文件格式推荐为 Netscape Cookie File、JSON 或 `.env`，但不要输出真实值。
   - 如果保存在工作区内，确保 `.secrets/` 或具体 Cookie 文件写入 `.gitignore`。

4. 读取 Cookie
   - agent 应通过 `eh_req_*` 工具请求，由工具从本地文件、环境变量或配置读取 Cookie。
   - 不要在聊天消息、工具参数、日志或最终回复中展示 Cookie 内容。
   - Cookie 只会在请求 E-Hentai/ExHentai 官方域名时附带。

## 推荐本地存储格式

### `.env`

使用环境变量名保存整段 Cookie：

- `EHENTAI_COOKIE=ipb_member_id=...; ipb_pass_hash=...; igneous=...; sk=...`

### JSON

保存为键值对象：

- `ipb_member_id`
- `ipb_pass_hash`
- `igneous`
- `sk`

### Netscape Cookie File

适合与下载器或 HTTP 客户端兼容。每行包含 domain、path、secure、expires、name、value 等字段。

## 域名与地址

| 用途 | EH 地址 | EX 地址 |
| --- | --- | --- |
| 首页/搜索 | `https://e-hentai.org/` | `https://exhentai.org/` |
| 官方 API | `https://api.e-hentai.org/api.php` | `https://exhentai.org/api.php` |
| 漫画详情 | `https://e-hentai.org/g/{gid}/{token}/` | `https://exhentai.org/g/{gid}/{token}/` |
| 图片页 | `https://e-hentai.org/s/{imageHash}/{gid}-{pageNo}` | `https://exhentai.org/s/{imageHash}/{gid}-{pageNo}` |
| 我的标签 | `https://e-hentai.org/mytags` | `https://exhentai.org/mytags` |
| 图片配额重置 | `https://e-hentai.org/home.php` | `https://exhentai.org/home.php` |

## 请求头

所有请求建议携带：

- `User-Agent`: 常规浏览器 UA。
- `Accept`: `text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8`。
- `Accept-Language`: 用户偏好语言，例如 `zh-CN,zh;q=0.9,en;q=0.8`。
- `Cookie`: 仅在用户已配置 Cookie 时添加。
- `Referer`: 图片页和图片直链请求建议设置为对应详情页或图片页。

## 标签中文映射

### Agent 本地工具

当前项目提供本地 EH 标签索引工具，agent 在执行本 skill 时应优先使用这些工具，而不是每次自行下载或扫描标签库：

- `eh_tag_load`: 加载或刷新 EhTagTranslation 数据库索引。程序启动时会自动从缓存加载；缓存不存在时从远程下载到本地。
- `eh_tag_search`: 根据中文名、英文 key、命名空间或简介搜索标签候选。
- `eh_tag_resolve_keyword`: 将中文标签解析为 E-Hentai 搜索表达式，例如 `中文` -> `language:chinese`。
- `eh_tag_translate`: 将 `{namespace, key}` 标签翻译为中文展示信息。

推荐调用规则：

1. 需要中文标签映射时，先调用 `eh_tag_resolve_keyword({ keyword, auto_select: true })`。
2. 若返回 `source=tagTranslation`，使用 `resolvedKeyword` 作为 `f_search`。
3. 若返回 `source=ambiguous`，向用户展示候选或结合上下文选择更明确的标签。
4. 若返回 `source=rawKeyword`，保留原关键词并可再尝试官方 `tagsuggest` API。
5. 查询详情后需要中文展示标签时，调用 `eh_tag_translate`。

默认缓存路径为 `data/eh_tag_db.html.json`，默认远程数据源为 EhTagTranslation 的 `db.html.json`。不要在日志中输出完整原始数据库。

### 数据源

优先使用 EhTagTranslation 数据库：

- `https://fastly.jsdelivr.net/gh/EhTagTranslation/DatabaseReleases/db.html.json`

### 输入

- `keyword`: 中文标签、英文标签或命名空间标签。
- `limit`: 默认 `100`。

### 输出

- `namespace`: 标签命名空间，例如 `language`、`female`、`artist`。
- `key`: E-Hentai 标签 key，例如 `chinese`。
- `translatedNamespace`: 中文命名空间。
- `tagName`: 中文标签名。
- `intro`: 标签说明。

### 处理规则

- 如果用户输入中文且命中翻译库，转换为 `{namespace}:{key}`。
- 如果用户输入已包含 `:`，例如 `language:chinese`，直接作为搜索表达式。
- 如果翻译库不可用，使用官方标签建议 API 回退。

## 官方标签建议 API

### 请求

- Method: `POST`
- URL: `https://api.e-hentai.org/api.php`
- Content-Type: `application/json`
- Agent 工具：优先调用 `eh_req_api`

### Body

- `method`: 固定为 `tagsuggest`
- `text`: 用户输入的最后一段关键词

### 返回处理

- 解析返回标签建议，取 `namespace` 与 `key`。
- 组合为 `{namespace}:{key}`，供搜索接口使用。

## 搜索漫画列表

### 请求

- Method: `GET`
- URL: `{siteBase}/`
- Agent 工具：优先调用 `eh_req_search`

### Query 参数

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `f_search` | 否 | 搜索表达式，例如 `language:chinese artist:name` |
| `page` 或 `p` | 否 | 搜索结果页码，从 `0` 开始；具体客户端可统一为 `p` |
| `prev` | 否 | 上一页 GID 游标 |
| `next` | 否 | 下一页 GID 游标 |
| `seek` | 否 | 日期跳转，格式 `yyyy-MM-dd` |
| `s_act` | 否 | 高级搜索可设为 `advanced` |

### 输出字段

- `gid`
- `token`
- `title`
- `japaneseTitle`
- `category`
- `pageCount`
- `uploadTime`
- `rating`
- `ratingCount`
- `uploader`
- `favoriteSlot`
- `galleryUrl`
- `prevGid`
- `nextGid`
- `totalCount`

## 获取漫画详情页

### 请求

- Method: `GET`
- URL: `{siteBase}/g/{gid}/{token}/`
- Agent 工具：优先调用 `eh_req_gallery`

### Query 参数

| 参数 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `p` | 否 | `0` | 缩略图分页页码 |
| `hc` | 否 | `1` | 是否显示完整评论，`1` 表示显示 |

### 输出字段

- `galleryUrl`
- `rawTitle`
- `japaneseTitle`
- `category`
- `cover`
- `pageCount`
- `rating`
- `realRating`
- `language`
- `uploader`
- `publishTime`
- `isExpunged`
- `tags`
- `ratingCount`
- `size`
- `favoriteCount`
- `torrentCount`
- `archivePageUrl`
- `comments`
- `thumbnails`
- `thumbnailsPageCount`

## 获取元数据 API

### 请求

- Method: `POST`
- URL: `https://api.e-hentai.org/api.php` 或 `https://exhentai.org/api.php`
- Content-Type: `application/json`
- Agent 工具：优先调用 `eh_req_api`

### Body

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `method` | 是 | 固定为 `gdata` |
| `gidlist` | 是 | `[[gid, token]]`，批量请求时最多建议 `25` 个 |
| `namespace` | 否 | 建议为 `1`，返回命名空间标签 |

### 用途

- 已有 `gid` 与 `token` 时快速获取标题、分类、标签、页数、封面、评分等信息。
- 不替代详情页缩略图解析；正文图片入口仍需从详情页或图片页解析。

## 获取正文图片入口

### 来源

详情页缩略图列表提供每页图片入口：

- `href`: `/s/{imageHash}/{gid}-{pageNo}` 或完整图片页 URL。
- `pageNo`: 图片页序号。
- `title`: 页码标题。

### 分页规则

- 详情页 `p=0` 通常返回前一批缩略图。
- 后续缩略图通过 `p=1`、`p=2` 继续获取。
- 每页数量可能是 `20`、`40` 或用户站点设置值，不能硬编码。

## 解析正文图片 URL

### 请求

- Method: `GET`
- URL: `{siteBase}/s/{imageHash}/{gid}-{pageNo}`
- Agent 工具：优先调用 `eh_req_image_page`

### Query 参数

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `nl` | 否 | reload key，用于重新加载图片或绕过失效图片页 |

### 输出字段

- `url`: 当前正文图片 URL。
- `width`: 当前图片宽度。
- `height`: 当前图片高度。
- `originalImageUrl`: 原图 URL，可能为空。
- `originalImageWidth`: 原图宽度，可能为空。
- `originalImageHeight`: 原图高度，可能为空。
- `reloadKey`: 重新加载 key。
- `imageHash`: 图片 hash。

### 正文图片发送

- 当需要向聊天发送漫画正文图片时，必须先调用 `eh_download_images` 下载已解析出的正文图片 URL，再调用 `send_forward_images` 发送返回的 `fileUrl` 列表。
- `eh_download_images` 的输入应使用已解析出的正文图片 URL 列表；若存在原图 URL 且用户要求优先原图，则优先传入 `originalImageUrl`，否则传入 `imageUrl`。
- `eh_download_images` 只有在整批图片全部下载成功时才会返回可发送的 `fileUrl`；若任意图片最终失败，本批次会整体视为失败，agent 不要调用 `send_forward_images`，也不要改用远端 webp 直链补发。
- `eh_download_images` 会复用 EH 请求代理配置并自动清理本地缓存，本地图片最多保留 100 张。
- 未经用户明确要求，不要一次性解析或发送整本漫画全部图片；应遵守 `imageLimit`。

## 图片下载请求

### 请求

- Method: `GET`
- URL: `imageUrl` 或 `originalImageUrl`

### 请求头

- `Referer`: 对应图片页 URL。
- `Cookie`: 如图片资源需要登录态，则附带同站点 Cookie。
- `Range`: 可选，用于断点续传，例如 `bytes=0-1023`。

## 端到端流程

1. 收集用户输入：`keyword`、`site`、`limit`、`imageLimit`。
2. 检查是否需要登录态；如果需要，引导用户把 Cookie 保存到本地安全位置。
3. 读取本地 Cookie，不在聊天中显示真实值。
4. 若启用标签翻译，先将中文标签映射为 `{namespace}:{key}`。
5. 若翻译失败且输入像标签，调用 `tagsuggest` API 获取建议。
6. 使用搜索页接口查询漫画列表。
7. 对前 `limit` 个结果请求详情页。
8. 从详情页解析缩略图入口。
9. 对前 `imageLimit` 个入口请求图片页，解析正文图片 URL。
10. 如需发送正文图片，先调用 `eh_download_images` 把正文图片 URL 下载成本地 `file:///` 地址。
11. 仅当 `eh_download_images.ok=true` 时，把返回项的 `fileUrl` 列表按原顺序传给 `send_forward_images` 合并转发，避免刷屏；若 `ok=false`，停止发送并返回失败原因。
12. 返回结构化结果和警告信息。

## 推荐返回格式

返回 JSON 风格对象：

- `query`: 用户原始关键词。
- `resolvedKeyword`: 实际搜索表达式。
- `site`: `EH` 或 `EX`。
- `source`: `translation`、`tagSuggestion` 或 `rawKeyword`。
- `cookieLoaded`: 布尔值，只表示是否加载，不返回内容。
- `results[]`: 漫画结果。
- `results[].gid`
- `results[].token`
- `results[].title`
- `results[].japaneseTitle`
- `results[].category`
- `results[].pageCount`
- `results[].rating`
- `results[].galleryUrl`
- `results[].tags`
- `results[].images[]`
- `results[].images[].pageNo`
- `results[].images[].imagePageUrl`
- `results[].images[].thumbnailHref`
- `results[].images[].imageUrl`
- `results[].images[].originalImageUrl`
- `results[].images[].width`
- `results[].images[].height`
- `warnings[]`

## 容错与提示

- `EX` 访问失败、空白页或跳转时，提示用户检查 `ipb_member_id`、`ipb_pass_hash`、`igneous` 是否有效。
- 搜索页无结果时，返回 `resolvedKeyword` 并建议用户换用英文标签或关闭标签翻译。
- 图片页返回 509 或配额图片时，停止批量解析并提示图片配额限制。
- 图片 URL 可能短期有效，下载时应尽快使用，并带上 `Referer` 与 Cookie。
- 网络失败最多重试 3 次；不要无限重试。
- 未经用户明确要求，不要解析整本漫画全部图片。
