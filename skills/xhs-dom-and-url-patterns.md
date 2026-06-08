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
| 点赞按钮 | `.engage-bar-style .like-wrapper` | force=true 穿透遮挡 |
| 收藏按钮 | `.engage-bar-style .collect-wrapper` | force=true 穿透遮挡 |

## JS 表达式最佳实践

`POST /api/evaluate` 的 `expression` 字段注意事项：

```javascript
// ✅ 好 — 简单表达式
"Array.from(document.querySelectorAll('img')).map(i => i.src)"

// ❌ 坏 — 嵌套引号和 includes 导致 is not defined 错误
"Array.from(document.querySelectorAll('img')).map(i => i.src).filter(s => s.includes('avatar'))"
// 上面这个在某些情况下会报错，原因是 Camoufox evaluate 的字符串解析问题

// ✅ 好 — 用双引号包裹，includes 用双引号
"Array.from(new Set(Array.from(document.querySelectorAll(\"img\")).map(i => i.src).filter(s => s.includes(\"xhscdn\") && !s.includes(\"avatar\") && !s.includes(\"platform\"))))"
```

## Camoufox API 端点速查

| 方法 | 端点 | Body | 说明 |
|------|------|------|------|
| POST | `/api/goto` | `{"url": "..."}` | 导航 |
| POST | `/api/click` | `{"selector": "...", "force": false}` | 点击元素 |
| POST | `/api/type` | `{"selector": "...", "text": "...", "delay": 100}` | 输入文本 |
| GET | `/api/html` | — | 获取页面 HTML |
| GET | `/api/screenshot` | — | 获取截图 base64 |
| POST | `/api/evaluate` | `{"expression": "..."}` | 执行 JS |
| POST | `/api/scroll` | `{"direction": "down", "distance": 500}` | 滚动页面 |
