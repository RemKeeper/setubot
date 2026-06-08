---
name: xhs-setu
description: 小红书涩图流程 — 浏览推荐页或按关键词搜索，筛选图片帖（跳过视频帖），点赞、收藏并通过 zerobot 图片消息/合并转发发送到聊天。
category: social-media
---

# 小红书涩图流程

## 触发条件

用户说"来点涩图"、"来N张涩图"、"涩图"、"找点XX涩图"、"来点XX的图"时触发此技能。
- 无具体主题 → 方案A（推荐页脚本）或方案B（手动推荐页）
- 指定主题（如"碧蓝航线"、"原神甘雨"、"cos蕾姆"）→ 方案C（关键词搜索）

## 方案选择

### 🚀 方案A（优先）：调用 `xhs_setu` 工具

当用户说"来点涩图"、"涩图"（无特定主题）时，调用：

```json
{"count": 1, "scroll": 2}
```

当用户说"来3张涩图"时，调用：

```json
{"count": 3, "scroll": 2}
```

当用户说"来点原神甘雨涩图"、"找点碧蓝航线的图"时，调用：

```json
{"count": 1, "scroll": 3, "keyword": "原神甘雨涩图"}
```

- 脚本通过 Camoufox API (127.0.0.1:58000) 操作浏览器
- 输出 JSON，从 `results[].images[]` 提取图片 URL
- 脚本完成后自动 POST 到 `http://127.0.0.1:8899/api/gallery`
- 当前项目会在 `xhs_setu` 工具内部自动发送图片：单图用 `message.Image(url)`，多图用合并转发，避免刷屏。
- 图片展示只走当前项目的 zerobot 图片消息/合并转发流程。

### 🔍 方案B：手动关键词搜索（仅工具失败时回退）

当用户要求特定主题（如"碧蓝航线涩图"、"原神cos"）时，推荐页无法按关键词筛选，需手动搜索：

1. **导航到搜索页**:
   ```
   POST /api/goto
   {"url": "https://www.xiaohongshu.com/search_result?keyword={URL编码的关键词}&source=web_search_result_notes"}
   ```

2. **滚动加载更多**: 用 `/api/scroll` 滚动2-3次获取更多帖子

3. **提取帖子列表**:
   ```
   POST /api/evaluate
   {"expression": "Array.from(document.querySelectorAll(\"section.note-item\")).map((s,i) => ({idx:i, title: s.querySelector(\".title\")?.textContent?.trim() || \"\", likes: s.querySelector(\".like-wrapper .count\")?.textContent?.trim() || \"\", hasVideo: !!s.querySelector(\"video\"), href: s.querySelector(\"a.cover\")?.href || \"\"})).filter(x => x.href && !x.hasVideo)"}
   ```

4. **逐个打开帖子** → 点赞 → 收藏 → 提取图片（同方案A步骤4-8）

**搜索页链接格式**: `search_result/帖子ID?xsec_token=...&xsec_source=`
**帖子详情页**: 将URL中的 `/search_result/` 改为 `/explore/` 即可打开独立详情页

### 🔄 方案C（回退）：Browser Tool 手动操作

**仅当脚本执行失败时**回退到逐步 browser tool 操作：

1. **浏览**: goto 小红书推荐页 `https://www.xiaohongshu.com/explore?channel_id=homefeed_recommend`
2. **筛选**: 用 `elements` action 获取帖子列表，筛选符合用户偏好的帖子
3. **提取链接**: 获取帖子的完整链接（含 xsec_token），将 `/explore/xxx` 改为 `/discovery/item/xxx`
4. **打开**: goto 到独立详情页（避免弹窗模式）
5. **点赞**: 点击 `.engage-bar-style .like-wrapper`
6. **收藏**: 点击 `.engage-bar-style .collect-wrapper`
7. **提取图片**: 用 `content` action 提取帖子中的图片URL
8. **发送图片**: 当前 agent 没有通用发图工具，优先回退为告知用户 `xhs_setu` 工具失败原因。

**重要**: 默认只操作 **1个** 帖子。仅当用户明确指定数量时才操作多个。

## DOM 选择器参考

| 目标 | 选择器 | 说明 |
|------|--------|------|
| 帖子卡片 | `section.note-item` | 搜索结果/推荐页的帖子容器 |
| 帖子链接 | `section.note-item a.cover` | 帖子封面链接，含 xsec_token |
| 帖子标题 | `.title` (在 note-item 内) | 帖子标题文本 |
| 点赞数 | `.like-wrapper .count` (在 note-item 内) | 帖子点赞数 |
| 视频检测 | `video` (在 note-item 内) | 判断是否为视频帖 |
| 点赞按钮 | `.engage-bar-style .like-wrapper` | 帖子详情页点赞，非评论区按钮 |
| 收藏按钮 | `.engage-bar-style .collect-wrapper` | 帖子详情页收藏 |
| 图片 | `notes_pre_post` 路径的 `<img>` 标签 | 用 `content` action 提取 markdown 中的图片链接 |
| 标签/hashtag | `/search_result?keyword=...` 链接 | 帖子中的话题标签 |

## 图片URL模式参考

| URL 片段 | 说明 | 是否保留 |
|----------|------|----------|
| `notes_pre_post` | 帖子主体图片 | ✅ 保留 |
| `spectrum` | 部分帖子图片 | ✅ 保留 |
| `comment` | 评论区图片 | ❌ 过滤掉 |
| `avatar` | 用户头像 | ❌ 过滤掉 |
| `platform` | 平台资源 | ❌ 过滤掉 |
| `w/120/format/webp` | 缩略图 | ❌ 过滤掉（找原图） |

## JS 表达式提取模式

**搜索结果帖子列表**:
```javascript
Array.from(document.querySelectorAll("section.note-item")).map((s,i) => ({
  idx:i,
  title: s.querySelector(".title")?.textContent?.trim() || "",
  likes: s.querySelector(".like-wrapper .count")?.textContent?.trim() || "",
  hasVideo: !!s.querySelector("video"),
  href: s.querySelector("a.cover")?.href || ""
})).filter(x => x.href && !x.hasVideo)
```

**帖子详情页图片**:
```javascript
Array.from(new Set(Array.from(document.querySelectorAll("img"))
  .map(i => i.src)
  .filter(s => s.includes("notes_pre_post") || s.includes("spectrum"))))
```

**帖子详情页图片（更宽泛）**:
```javascript
Array.from(new Set(Array.from(document.querySelectorAll("img"))
  .map(i => i.src)
  .filter(s => s.includes("xhscdn") && !s.includes("avatar") && !s.includes("platform") && !s.includes("comment"))))
```

## 🏷️ 关键词搜索脚本能力

`xhs_setu.py` 支持 `--keyword` 参数。当用户指定具体主题（如"碧蓝航线"、"原神甘雨"）时，优先调用 `xhs_setu` 工具并传入 keyword。

```bash
# 1. 导航到搜索页（keyword 需要 URL 编码）
curl -s -X POST http://127.0.0.1:58000/api/goto -H "Content-Type: application/json" \
  -d '{"url": "https://www.xiaohongshu.com/search_result?keyword=碧蓝航线涩图&source=web_search_result_notes"}'

# 2. 等待加载后提取帖子链接
sleep 3
curl -s -X POST http://127.0.0.1:58000/api/evaluate -H "Content-Type: application/json" \
  -d '{"expression": "Array.from(document.querySelectorAll(\"section.note-item a.cover\")).slice(0,10).map(a => a.href)"}'

# 3. 提取标题和点赞数用于筛选
curl -s -X POST http://127.0.0.1:58000/api/evaluate -H "Content-Type: application/json" \
  -d '{"expression": "Array.from(document.querySelectorAll(\"section.note-item\")).slice(0,10).map(s => ({title: s.querySelector(\".title\")?.textContent?.trim() || \"\", likes: s.querySelector(\".like-wrapper .count\")?.textContent?.trim() || \"\", hasVideo: !!s.querySelector(\"video\")}))"}'

# 4. 逐个帖子：导航 → 点赞 → 收藏 → 提取图片（见下方图片提取规则）
```

**逐帖子操作循环**（对每个目标帖子）：
1. `POST /api/goto` 导航到帖子 URL
2. `sleep 3` 等待加载
3. `POST /api/click` 点赞 `.engage-bar-style .like-wrapper`（force=true）
4. `sleep 1`
5. `POST /api/click` 收藏 `.engage-bar-style .collect-wrapper`（force=true）
6. `sleep 1`
7. `POST /api/evaluate` 提取图片（见图片提取 JS）
8. 由 `xhs_setu` 工具内部用 zerobot 图片消息/合并转发发送图片

**图片提取 JS 表达式**：
```json
{"expression": "Array.from(new Set(Array.from(document.querySelectorAll(\"img\")).map(i => i.src).filter(s => s.includes(\"xhscdn\") && !s.includes(\"avatar\") && !s.includes(\"platform\"))))"}
```
- 帖子图片 URL 包含 `notes_pre_post` 或 `spectrum`
- 头像 URL 包含 `avatar`，平台资源包含 `platform` — 都要排除
- 后缀 `nd_dft_wlteh_jpg_3` 或 `nd_dft_wgth_jpg_3` 是高质量原图

## 关键技巧

- 推荐页提取带 `xsec_token` 的链接
- `/explore/xxx` → `/discovery/item/xxx` 转到独立详情页（避免 explore 弹窗模式）
- 点赞/收藏使用 `force=True` 穿透遮挡层
- `dismiss_popups()` 用 JS evaluate 关闭弹窗/遮罩，覆盖确认按钮、dialog/modal、popup 容器
- `api_evaluate(expression)` 调用 `/api/evaluate` 在页面执行任意 JS
- 图片提取用 JS evaluate 的 `querySelectorAll('img')`（解决懒加载导致 0 张图片的问题）
- 检测 `<video>` 元素跳过视频帖，从候选队列补选图片帖
- **JS 表达式转义问题**: `/api/evaluate` 的 expression 中避免嵌套双引号，用单引号代替。复杂表达式建议先在浏览器console测试
- **搜索页URL编码**: 关键词需URL编码，如 `%E7%A2%A7%E8%93%9D%E8%88%AA%E7%BA%BF` = 碧蓝航线
- **图片去重**: 搜索结果中同一张图片可能以不同尺寸/格式出现多次，用 `new Set()` 去重后过滤掉缩略图（含 `w/120`）
- **帖子打开顺序**: 搜索结果中的帖子可以并行处理（独立打开、点赞、收藏、提取），但需注意 xsec_token 有效期
- **JS 表达式转义陷阱**：`/api/evaluate` 的 expression 字段中不要用单引号包裹含 `includes()` 的字符串，会导致 `is not defined` 错误。用简单表达式，避免复杂嵌套引号
- **视频帖占比高**: 搜索结果中高赞帖子很多是视频帖（特别是cosplay类），图片提取会返回 `"video"` 或空数组。建议：(1) 在搜索结果列表就用 `hasVideo` 过滤掉视频帖；(2) 如果连续3+个帖子都是视频，换搜索关键词或滚动加载更多
- **图片容器变体**: 部分帖子的图片不在 `notes_pre_post` 路径下，而是在 `spectrum` 路径或无特定路径标记。用宽泛过滤 `s.includes("xhscdn") && !s.includes("avatar") && !s.includes("platform") && !s.includes("comment")` 兜底
- **详情页图片为空**: 帖子详情页有时图片以 base64 data URL 加载（`data:image/png;base64,...`），此时 `<img>` 的 `src` 不含 `xhscdn`，需要检查 `data-src` 属性或等待懒加载完成

## Gallery 集成

- API: `POST /api/gallery` 推送图片，`GET /` 查看画廊
- QQ 聊天内展示由 `xhs_setu` 工具直接完成，Gallery 只作为额外归档展示。

## xhs_dislike.py — 取消点赞收藏

```json
{}
{"keyword": "关键词"}
```

用户说"不喜欢"时优先调用 `xhs_dislike` 工具；只有用户明确说"以后别推 X"时才传入 `keyword`。

### ⚠️ 关键限制：必须立即执行

`xhs_dislike.py` 操作的是**当前浏览器打开的帖子**。帖子 URL 中的 `xsec_token` 会在**几分钟内过期**，之后无法重新导航到该帖子。

**正确的"不喜欢"工作流**：
1. 用户说"不喜欢"时，**立即**用 Camoufox API 导航到上一个帖子的 URL（来自 `xhs_setu.py` 输出的 `results[].url`）
2. 等待 3 秒让页面加载
3. **立即**运行 `python3 xhs_dislike.py`
4. 如果导航后页面标题不匹配（显示了其他帖子），说明 token 已过期，**直接告诉用户链接已过期**，不要反复重试

**已验证的失败模式（不要重试）**：
- `window.location.href = post_url` → 重定向到 `/explore/` 首页
- `/api/goto` → 同样重定向
- 通过搜索标题找帖子 → 搜索结果不包含该帖子
- 在用户收藏/点赞页面找帖子 → **刚操作的帖子不会立即同步到个人页面**（有延迟）
- 通过 XHS 内部 API (`/api/sns/web/v1/note/like`) 直接调用 → 缺少 `x-s`/`x-t` 签名，请求被拒

**替代方案**：如果 token 已过期无法取消，建议用户使用 `--keyword` 将相关关键词加入负面列表，避免未来再出现类似内容。

## Camoufox API 端点

| 方法 | 端点 | 功能 |
|------|------|------|
| POST | `/api/goto` | 导航到 URL |
| POST | `/api/click` | 点击元素，支持 `force` 参数 |
| POST | `/api/scroll` | 滚动页面 |
| GET | `/api/html` | 获取页面 HTML |
| POST | `/api/evaluate` | 在当前页面执行 JS，返回 `{result: ...}` |

## 用户筛选偏好

- ✅ 喜欢: 二次元、cosplay、动漫、游戏角色、涩图（擦边/性感/可爱女生cosplay）
- ❌ 排斥: 脱毛、医美、整形、激光、护肤、广告、推广、探店等商业推广内容

> ⚠️ **负面关键词规则**: 仅当用户明确指示时才添加负面关键词，绝不自行扩展。
