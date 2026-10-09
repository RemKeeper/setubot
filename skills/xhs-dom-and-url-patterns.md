# 小红书 DOM 选择器与 URL 模式参考

## 搜索结果页 DOM

搜索结果 URL: `https://www.xiaohongshu.com/search_result?keyword=关键词&source=web_search_result_notes`

| 目标 | 选择器 | 说明 |
|------|--------|------|
| 帖子卡片 | `section.note-item` | 每个搜索结果卡片 |
| 帖子链接 | `section.note-item a.cover` | 帖子封面链接，href 含 xsec_token |
| 帖子标题 | `section.note-item .title` | 可能为空 |
| 点赞数 | `section.note-item .like-wrapper .count` | |
| 视频标记 | `section.note-item video` | 存在则为视频帖，应跳过 |

## 详情页 DOM

所有详情查询必须限定在 `#noteContainer`，避免抓到底层 Feed。正文图片的唯一目标作用域是：

```css
#noteContainer .media-container .swiper-slide:not(.swiper-slide-duplicate) .note-slider-img img
```

兼容图片节点层级变化时，只可在同一媒体和非克隆 Slide 作用域内回退到：

```css
#noteContainer .media-container .swiper-slide:not(.swiper-slide-duplicate) img
```

禁止全页遍历 `img`。这会混入 Feed 封面、作者/评论头像、评论配图及 Emoji。`.swiper-slide-duplicate` 是 Swiper 循环克隆节点，必须排除。

## 图片 URL 模式

小红书 CDN 域名: `sns-webpic-qc.xhscdn.com`

| URL 特征 | 含义 | 是否要保留 |
|-----------|------|-----------|
| `notes_pre_post/` | 帖子正文图片 | ✅ 保留 |
| `spectrum/` | 帖子正文图片（另一种路径） | ✅ 保留 |
| `avatar/` | 用户头像 | ❌ 排除 |
| `platform/` | 平台静态资源 | ❌ 排除 |
| `comment/` | 评论区图片 | ❌ 排除（通常不是目标） |
| `nc_n_webp_mw_1` | 缩略图后缀 | ❌ 排除（低质量） |
| `nd_dft_wlteh_jpg_3` | 高质量原图后缀 | ✅ 优先 |
| `nd_dft_wgth_jpg_3` | 高质量原图后缀（另一种） | ✅ 优先 |

## 推荐页帖子 DOM

| 目标 | 选择器 | 说明 |
|------|--------|------|
| 帖子卡片 | `section.note-item` | 标题和链接必须在同一个卡片内提取，禁止分别收集后按数组下标拼接 |
| 帖子链接 | `section.note-item a.cover` | 逐卡片读取 `href` |
| 帖子标题 | `section.note-item .title` | 仅作为候选标题，进入详情后以 `#noteContainer h1.title` 为准 |
| 点赞按钮 | `#noteContainer .engage-bar-style .like-wrapper` | 激活 class 为 `like-active`；force=true 穿透遮挡 |
| 收藏按钮 | `#noteContainer .engage-bar-style .collect-wrapper` | 激活 class 为 `collect-active`；force=true 穿透遮挡 |

进入详情页后必须从最终 `location.pathname` 提取帖子 ID，并与候选 ID 对比。ID 不一致时不得发送图片；标题必须使用当前 `#noteContainer h1.title`，防止重定向、旧容器或卡片列表变化导致错配。

## JS 表达式最佳实践

`POST /api/evaluate` 的 `expression` 字段注意事项：

复杂表达式应作为合法 JSON 字符串传给 `/api/evaluate`。选择器必须限定详情正文范围，不能通过全页 URL 过滤代替 DOM 作用域过滤。Python 中建议使用三引号保存 JavaScript，再由 HTTP 客户端的 `json={"expression": expression}` 负责转义。

## Camoufox API 端点速查

| 方法 | 端点 | Body | 说明 |
|------|------|------|------|
| POST | `/api/act` | `{"action": "goto", "url": "..."}` | 统一动作接口：goto/click/click_text/fill/type/press/hover/select/scroll/wait/wait_selector/back/forward/reload |
| GET | `/api/observe` | — | 页面状态与可交互元素 |
| GET | `/api/markdown` | — | 正文转 Markdown |
| GET | `/api/html` | — | 获取页面 HTML |
| GET | `/api/screenshot` | — | 获取截图 base64 |
| POST | `/api/evaluate` | `{"expression": "..."}` | 执行 JS |
