---
name: reverse-image-search
description: "以图搜图 / 图片出处反查 — 用户问「这图出处 / 哪来的 / 作者是谁 / 求原图 / 求高清 / 这是哪部番」时，优先用浏览器调用 SauceNAO、ascii2d、IQDB、trace.moe、Google Lens、Yandex、Bing、TinEye 等以图搜图引擎按图片链接检索，汇总最详细出处与多个候选网址；能拿到原始出处最高分辨率版本时直接发送图片并附带信息。"
category: search
---

# 以图搜图 · 图片出处反查

## 触发条件

用户发送/引用一张图片，并且意图属于以下任一类：

- 「出处」「这图哪来的」「谁画的」「作者是谁」「求原图」「求高清」「求大图」「源」「sauce」
- 「这是哪部番 / 第几集」「这是谁（coser / 角色）」「这张图在哪个网站」

## 核心原则（必须遵守）

1. **以图搜图优先，禁止「先描述再按关键词搜」**。视觉模型描述 → 关键词搜索会严重跑偏，只允许作为所有引擎都失败后的最后兜底，且结果必须标注「低置信，仅供参考」。
2. `analyze_images` 在本流程中**只用于判断图片类型**（插画 / 动画截图 / 漫画页 / 真人照片 / cos / 表情包），以便选择引擎；**不得**把它的描述当搜索关键词，也不得把它的猜测当作出处结论。
3. 出处结论必须来自搜图引擎命中的**具体网页链接**，不得编造作者名、作品名、链接。
4. 每次 `browser_task` 只做**一个引擎**的检索（子代理步数有限，且多引擎混在一起容易半途失败），由主 agent 汇总。
5. 引擎之间互相印证：两个及以上引擎指向同一作品 / 作者，才可标为「高置信」。

## 第 0 步：拿到图片链接

按优先级取得可公网访问的图片 URL（下文记为 `IMG`）：

1. 用户消息中的「本轮图片的可访问原始链接」或图片占位符里的「原始链接」。
2. `analyze_images` 返回结果末尾附带的图片原始链接。
3. 用户在文字里直接给出的图片 URL / 帖子 URL（帖子 URL 应先打开取出图片直链）。

要求：

- 链接中的 `&amp;` 必须还原成 `&`。
- 拼进搜图引擎 URL 时**必须整体 `encodeURIComponent`**，否则 QQ 图片链接里的 `&rkey=...` 会被截断，引擎拿到的是坏图。
- 推荐让子代理在页面里用 JS 跳转，避免手工编码出错：
  `location.href = 'https://saucenao.com/search.php?db=999&url=' + encodeURIComponent('IMG')`
- QQ 图片链接（`multimedia.nt.qq.com.cn`、`gchat.qpic.cn` 等）有时效性。若多个引擎都提示「无法获取图片 / invalid image / 403」，说明链接已过期或引擎无法访问，应告知用户重新发送原图，或发送图片所在帖子的链接。

## 第 1 步：选择引擎

| 图片类型 | 首选 | 补充 |
| --- | --- | --- |
| 二次元插画 / 同人图 / 立绘 | SauceNAO | ascii2d（色合/特征）、IQDB |
| 动画截图 | trace.moe | SauceNAO |
| 漫画页 | SauceNAO | IQDB、Google Lens |
| 真人照片 / cos / 写真 / 风景 / 实物 | Google Lens | Yandex、Bing、TinEye |
| 表情包 / 梗图 / 截图转载 | Google Lens | Yandex、TinEye |
| 只想要更高分辨率版本 | 先确定出处，再用 TinEye（按尺寸排序）或 Yandex「其他尺寸」 | Danbooru / Yande.re 原图 |
| 无法判断类型 | SauceNAO | Google Lens |

执行顺序：

1. 先跑首选引擎。
2. 首选引擎有高置信结果（见第 3 步阈值）时，直接进入第 4 步深挖出处，最多再用 1 个补充引擎做交叉验证。
3. 首选引擎无结果或只有低置信结果时，依次尝试补充引擎，**最多 4 个引擎**后停止。
4. 全部失败才进入第 6 步兜底。

## 第 2 步：各引擎 browser_task 模板

调用 `browser_task` 时，`goal` 必须写完整，包括图片原始链接、跳转方式、提取方法和返回格式，因为子代理看不到主对话。下面的 `IMG` 替换为真实图片链接（未编码的原始形式，由子代理在 JS 中 `encodeURIComponent`）。

返回格式统一要求子代理输出：

```
引擎：xxx
状态：成功 / 无结果 / 被拦截(验证码/Cloudflare) / 图片无法获取
结果（按相似度从高到低，最多 8 条）：
1. 相似度：xx% | 标题/作者/作品：xxx | 尺寸：WxH | 链接：url1, url2
...
最终页面 URL：xxx
```

### SauceNAO（二次元首选）

```
goal: 用 SauceNAO 反查图片出处。
1. 先访问 https://saucenao.com/ ，然后 browser_evaluate 执行：
   location.href = 'https://saucenao.com/search.php?db=999&url=' + encodeURIComponent('IMG')
2. browser_wait 3000，等待结果页加载（出现 .result 元素）。
3. browser_evaluate 提取：
   JSON.stringify(Array.from(document.querySelectorAll('.result:not(.hidden) .resulttable')).slice(0,8).map(t=>({sim:t.querySelector('.resultsimilarityinfo')?.textContent.trim(),title:t.querySelector('.resulttitle')?.innerText.trim(),info:t.querySelector('.resultcontentcolumn')?.innerText.trim().slice(0,300),links:[...new Set(Array.from(t.querySelectorAll('.resultmiscinfo a, .resultcontentcolumn a')).map(a=>a.href))].slice(0,6)})))
4. 若可见结果为空，再提取 .result.hidden 中的前 3 条，标记为「低相似度」。
5. 若页面提示 limit / Daily Search Limit Exceeded，返回「被限流」。
按统一格式返回。
```

### ascii2d（二次元补充，对裁剪/改色更鲁棒）

```
goal: 用 ascii2d 反查图片出处。
1. 访问 https://ascii2d.net/ ，然后 browser_evaluate 执行：
   location.href = 'https://ascii2d.net/search/url/' + encodeURIComponent('IMG')
2. 等待跳转到 /search/color/ 结果页（遇到 Cloudflare 验证页先 browser_wait 5000 再观察一次，仍拦截则返回「被拦截」）。
3. browser_evaluate 提取色合结果（第 1 个 item-box 是查询图本身，跳过）：
   JSON.stringify(Array.from(document.querySelectorAll('.item-box')).slice(1,7).map(b=>({info:b.querySelector('.info-box small')?.textContent.trim(),links:Array.from(b.querySelectorAll('.detail-box a')).map(a=>({t:a.textContent.trim(),h:a.href})).slice(0,6)})))
4. browser_evaluate 执行 location.href = location.href.replace('/color/','/bovw/') 进入特征检索，等待后用同样表达式再提取一次。
5. 分别标注「色合」「特征」两组结果返回。
```

### IQDB（二次元补充，主要命中 Danbooru / Gelbooru / Yande.re 等图站）

```
goal: 用 IQDB 反查图片。
1. browser_goto https://iqdb.org/ ，然后 browser_evaluate 执行：
   location.href = 'https://iqdb.org/?url=' + encodeURIComponent('IMG')
2. 等待结果出现后 browser_evaluate 提取（第 1 个表是 Your image，跳过）：
   JSON.stringify(Array.from(document.querySelectorAll('#pages > div table')).slice(1,7).map(t=>({link:t.querySelector('a')?.href,rows:Array.from(t.querySelectorAll('tr')).map(r=>r.innerText.trim()).filter(Boolean)})))
3. 每条结果中的 "xx% similarity" 为相似度，"WxH [Safe/Explicit]" 为尺寸和分级。
```

### trace.moe（动画截图首选）

```
goal: 用 trace.moe 识别动画截图来源。
1. browser_goto 'https://api.trace.moe/search?anilistInfo&cutBorders&url=' + 已 encodeURIComponent 的 IMG
   （可先访问 https://trace.moe/ 再用 browser_evaluate 设置 location.href 完成编码跳转）
2. browser_evaluate 解析 JSON：
   (()=>{const j=JSON.parse(document.body.innerText);return JSON.stringify({error:j.error,result:(j.result||[]).slice(0,3).map(r=>({sim:(r.similarity*100).toFixed(1)+'%',native:r.anilist?.title?.native,romaji:r.anilist?.title?.romaji,english:r.anilist?.title?.english,anilistId:r.anilist?.id,isAdult:r.anilist?.isAdult,episode:r.episode,from:r.from,to:r.to,image:r.image}))})})()
3. 把 from/to 秒数换算为 mm:ss 返回。
```

trace.moe 相似度低于 90% 基本不可信，按「未命中」处理。

### Google Lens（真人照片 / 通用图片首选）

```
goal: 用 Google Lens 反查图片出处。
1. browser_goto https://www.google.com/ ，然后 browser_evaluate 执行：
   location.href = 'https://lens.google.com/uploadbyurl?url=' + encodeURIComponent('IMG')
2. 等待 3000 毫秒，用 browser_observe 查看页面；若出现同意条款弹窗，点击「全部接受 / Accept all」。
3. 优先点击「完全匹配的结果 / Exact matches」标签（用 browser_click_text）；没有该标签时使用「视觉匹配 / Visual matches」。
4. browser_evaluate 提取外部链接：
   JSON.stringify([...new Map(Array.from(document.querySelectorAll('a[href^="http"]')).filter(a=>!/(^|\.)google\.|gstatic\./.test(new URL(a.href).hostname)).map(a=>[a.href,{t:a.innerText.trim().replace(/\s+/g,' ').slice(0,100),h:a.href}])).values()].slice(0,15))
5. 若页面显示图片尺寸（如 1920x1080），一并记录。
```

### Yandex（真人 / cos / 找更大尺寸）

```
goal: 用 Yandex 以图搜图。
1. browser_goto https://yandex.com/images/ ，然后 browser_evaluate 执行：
   location.href = 'https://yandex.com/images/search?rpt=imageview&url=' + encodeURIComponent('IMG')
2. 等待 3000 毫秒。若出现验证码（SmartCaptcha / "Are you not a robot"），直接返回「被拦截」，不要尝试破解。
3. 用 browser_markdown 或 browser_observe 读取页面，重点提取：
   - 「Sites / 包含此图片的网站」区块中的标题和链接（最多 10 条）
   - 「Other sizes / 其他尺寸」中列出的尺寸及对应原图链接，按面积从大到小排列
```

### Bing 视觉搜索（通用补充）

```
goal: 用 Bing 视觉搜索反查图片。
1. browser_goto https://www.bing.com/ ，然后 browser_evaluate 执行：
   location.href = 'https://www.bing.com/images/search?view=detailv2&iss=sbi&form=SBIVSP&sbisrc=UrlPaste&q=imgurl:' + encodeURIComponent('IMG')
2. 等待 3000 毫秒，点击「包含此图像的页面 / Pages with this image」（用 browser_click_text）。
3. 提取页面标题与链接（最多 10 条），以及可见的图片尺寸。
```

### TinEye（精确匹配、找最高分辨率版本）

```
goal: 用 TinEye 查找同一图片的所有版本，并按尺寸从大到小排序。
1. browser_goto https://tineye.com/ ，然后 browser_evaluate 执行：
   location.href = 'https://tineye.com/search?sort=size&order=desc&url=' + encodeURIComponent('IMG')
2. 等待 3000 毫秒，用 browser_markdown 提取每条匹配的网站、页面链接、图片直链、尺寸（WxH）、首次发现日期，最多 10 条。
3. 页面显示 0 results 时返回「无结果」。
```

## 第 3 步：汇总与置信度

| 引擎 | 高置信 | 中置信 | 低置信 / 视为未命中 |
| --- | --- | --- | --- |
| SauceNAO | ≥ 85% | 70% – 85% | < 70% |
| IQDB | ≥ 90% | 80% – 90% | < 80% |
| trace.moe | ≥ 93% | 90% – 93% | < 90% |
| ascii2d | 色合与特征检索都指向同一作品 | 只有一项命中 | 结果互不相关 |
| Google / Yandex / Bing / TinEye | 多个独立站点指向同一作品或作者 | 单站点命中 | 只有「视觉相似」的无关图 |

合并规则：

- 以「作品 ID / 作者」为键去重，例如 Pixiv illust ID、推文 ID、Danbooru post ID。
- 原始出处优先级：作者本人发布页（Pixiv / X(Twitter) / Fanbox / Fantia / Skeb / 米游社 / 微博 / Lofter / ArtStation / DeviantArt / Instagram 等）＞ 图站（Danbooru / Gelbooru / Yande.re / Konachan / Zerochan）＞ 转载站（Pinterest、贴吧、壁纸站等）。
- 图站条目的 `source` 字段通常指向作者原帖，要顺着它找到真正的原始出处。

## 第 4 步：深挖原始出处与最高分辨率版本

取第 3 步中置信度最高的 1–2 个候选，用 `browser_task` 打开原始页面，提取详细信息和原图直链。

### Pixiv

```
goal: 获取 Pixiv 作品 ILLUST_ID 的详细信息与原图地址。
1. browser_goto https://www.pixiv.net/artworks/ILLUST_ID
2. browser_evaluate：
   fetch('/ajax/illust/ILLUST_ID').then(r=>r.json()).then(j=>{if(j.error)return JSON.stringify({error:j.message});const b=j.body;return JSON.stringify({title:b.title,user:b.userName,userId:b.userId,date:b.createDate,width:b.width,height:b.height,pageCount:b.pageCount,xRestrict:b.xRestrict,ai:b.aiType,bookmarks:b.bookmarkCount,tags:b.tags.tags.slice(0,15).map(t=>t.tag+(t.translation&&t.translation.en?'('+t.translation.en+')':'')),original:b.urls&&b.urls.original})})
3. 若 pageCount > 1，再执行：
   fetch('/ajax/illust/ILLUST_ID/pages').then(r=>r.json()).then(j=>JSON.stringify(j.body.map((p,i)=>({p:i,w:p.width,h:p.height,original:p.urls.original}))))
4. 作品已删除或 R-18 未登录导致 original 为空时如实返回。
```

- `i.pximg.net` 原图有防盗链（必须带 `Referer: https://www.pixiv.net/`），**不能直接交给发图工具**。发送时把域名替换为公开反代：`https://i.pximg.net/img-original/...` → `https://i.pixiv.re/img-original/...`；也可用 `https://pixiv.re/{ILLUST_ID}.jpg`（多图为 `{ILLUST_ID}-{页码}.jpg`，页码从 1 开始）。
- 有多张图时，要确认命中的是哪一页（SauceNAO 结果中的 `_p0` / `_p1` 会注明页码）。

### X / Twitter

- 推文图片原图直链格式为 `https://pbs.twimg.com/media/{MEDIA_ID}?format=jpg&name=orig`，`format` 按原图格式填 `jpg` 或 `png`。
- 未登录时推文页可能看不到内容，可改用 `https://fxtwitter.com/{user}/status/{id}` 或 `https://vxtwitter.com/...` 读取正文和图片链接。
- 需要提取的信息：作者显示名与 @ID、发布时间、推文正文、图片原图直链。

### Danbooru

```
goal: 读取 Danbooru 帖子 POST_ID 的信息。
browser_goto https://danbooru.donmai.us/posts/POST_ID.json ，browser_evaluate：
(()=>{const j=JSON.parse(document.body.innerText);return JSON.stringify({artist:j.tag_string_artist,copyright:j.tag_string_copyright,character:j.tag_string_character,rating:j.rating,width:j.image_width,height:j.image_height,size:j.file_size,ext:j.file_ext,file_url:j.file_url,large:j.large_file_url,source:j.source,pixiv_id:j.pixiv_id,created:j.created_at})})()
```

### Yande.re / Konachan

```
browser_goto https://yande.re/post.json?tags=id:POST_ID （Konachan 同理：https://konachan.com/post.json?tags=id:POST_ID）
browser_evaluate：
(()=>{const j=JSON.parse(document.body.innerText)[0];return JSON.stringify({tags:j.tags,rating:j.rating,width:j.width,height:j.height,file_url:j.file_url,jpeg_url:j.jpeg_url,file_size:j.file_size,source:j.source})})()
```

### Gelbooru / Zerochan / 其他站点

打开帖子页，用 `browser_observe` 找「Original image / 原图 / Download」链接，同时提取尺寸、标签和 source 字段。

### 选取最高分辨率版本

- 比较所有候选的 `width × height`，选面积最大的一张；面积相同时优先作者原帖的原图，其次图站 `file_url`。
- 搜图引擎结果页里的缩略图（SauceNAO / ascii2d / IQDB 的预览图、Google 的 `encrypted-tbn` 链接）**不算原图**，禁止当作高清版本发送。
- 无法确认分辨率时如实说明「未能确认是否为最高分辨率」。

## 第 5 步：交付给用户

### 发送图片预览

满足以下全部条件时，调用 `send_forward_images` 发送图片，单张时 `images` 只传 1 个链接，工具会以普通图片消息发出：

- 已拿到原图直链，并且不是搜索引擎缩略图。
- 链接无需 Referer 或 Cookie 即可访问（Pixiv 已换成 `i.pixiv.re`，Twitter 已带 `name=orig`，图站为 `file_url`）。
- 单张不超过约 20MB；Yande.re 等站的 PNG 原图过大时改用 `jpeg_url`，并在文字中注明。

Pixiv 多图且用户未指定页码时，只发送命中的那一页；用户要求全部时，最多发送 30 张。

若 `send_forward_images` 返回失败（例如反代不可用或图片过大），不要反复重试同一链接：换一个候选直链再试 1 次，仍失败就只发文字信息，并附上原图链接。

### 文字结果格式

QQ 不渲染 Markdown，使用纯文本，保持紧凑：

```
🔍 出处查询结果（置信度：高 / 中 / 低）
作品：《标题》
作者：作者名（平台 @ID）
发布：2025-03-01
原帖：https://www.pixiv.net/artworks/123456
原图：3000×4000 PNG（已发送预览）
角色/作品：角色名 / 所属作品（如有）
标签：tag1, tag2, tag3（最多 8 个）

其他可能的结果：
2. [SauceNAO 82%] Danbooru #1234567 https://danbooru.donmai.us/posts/1234567
3. [ascii2d] X @xxx https://x.com/xxx/status/...
4. [Google Lens] 某壁纸站转载 https://...

检索引擎：SauceNAO ✓ / ascii2d ✓ / Google Lens ✗(无结果)
```

要求：

- 最可信的出处放第一位，然后列出 2–5 个其他可能的结果网址，每条注明来源引擎与相似度。
- 多个结果之间存在矛盾时，明确说明分歧，不要强行下结论。
- 动画截图需写明：番剧名（中文 / 日文 / 罗马音）、第几集、时间点 mm:ss。
- 真人 / cos 图片只给出发布页面和发布者，**不要**推测普通人的真实身份或隐私信息。
- 被限流、出现验证码或图片链接失效时，直接告诉用户是哪个引擎、什么原因。

## 第 6 步：兜底（所有以图搜图引擎都失败时）

1. 先确认失败原因：如果是图片链接失效，请用户重新发送原图或帖子链接，不要直接兜底。
2. 确实没有任何匹配时，才可以用 `analyze_images` 提取**图中可见的硬信息**：签名或水印文字、画师 ID、网站水印、角色名、画面中出现的文字。
3. 只使用这些硬信息作为关键词，通过 `exa_search` 或浏览器搜索；禁止使用「蓝发少女、校服、樱花背景」这类主观画面描述作为关键词。
4. 兜底结果必须标注「⚠️ 未找到以图搜图匹配，以下为根据图中文字/水印推测，低置信」。

## 常见陷阱

- **链接未编码**：QQ 图片 URL 中含 `&`，直接拼接会导致引擎只收到半截 URL，表现为「无结果」或「invalid image」。务必使用 `encodeURIComponent`。
- **把缩略图当原图**：SauceNAO、IQDB、ascii2d 结果页中的图片都是缩略图，必须进入原帖或图站获取 `original` 或 `file_url`。
- **Pixiv 防盗链**：`i.pximg.net` 直接发送会失败（403），必须换成 `i.pixiv.re`。
- **SauceNAO 限流**：匿名用户每 30 秒约 4 次、每天约 100 次；被限流时改用 ascii2d、IQDB，不要反复刷新。
- **ascii2d / Yandex 人机验证**：遇到 Cloudflare 或验证码时最多等待一次，仍被拦截就换引擎，不要尝试破解验证码。
- **一个 browser_task 塞多个引擎**：容易超出子代理步数导致全部失败，坚持一个任务一个引擎。
- **只给一个结果**：即使已有高置信结果，也要附带其他候选网址，方便用户自行核对。
