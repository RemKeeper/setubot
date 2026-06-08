#!/usr/bin/env python3
"""
小红书涩图一键操作脚本
通过 Camoufox API 完成：浏览推荐页/关键词搜索 → 筛选帖子 → 点赞 → 收藏 → 提取图片URL

用法:
    # 推荐页模式（默认）
    python3 xhs_setu.py                        # 默认操作1个帖子
    python3 xhs_setu.py --count 3              # 操作3个帖子
    python3 xhs_setu.py --count 5 --scroll 8   # 操作5个，滚动8次加载更多

    # 关键词搜索模式
    python3 xhs_setu.py -k "碧蓝航线cos"                      # 搜索关键词，默认5个帖子
    python3 xhs_setu.py -k "原神甘雨" --count 3                # 搜索，指定数量
    python3 xhs_setu.py -k "蔚蓝档案普拉娜" --count 5 --scroll 8  # 搜索+滚动
    python3 xhs_setu.py -k "流萤cos" --seen ids.txt            # 搜索+去重已处理帖子

输出: JSON 格式，包含每个帖子的标题和图片URL列表
"""

import argparse
import json
import re
import sys
import time
import urllib.parse

import httpx

API_BASE = "http://127.0.0.1:58000"
GALLERY_API = "http://127.0.0.1:8899/api/gallery"
TIMEOUT = 30.0
CLICK_TIMEOUT = 8.0

# ── 用户偏好关键词 ──────────────────────────────────────────────────
# 普通权重关键词 — 权重1
PREFER_KEYWORDS = [
    "cos", "cosplay", "coser",
    "二次元", "动漫", "漫展", "漫画",
    "jk", "写真", "私房", "约拍",
    "性感", "辣妹", "御姐", "萝莉", "少女",
    "泳装", "比基尼", "黑丝", "白丝", "丝袜", "网袜",
    "碧蓝", "原神", "崩坏", "明日方舟", "蔚蓝档案", "碧蓝档案",
    "王者", "fate", "fgo", "lol", "英雄联盟",
    "甘雨", "刻晴", "雷电将军", "芙宁娜", "纳西妲", "胡桃",
    "2b", "尼尔", "蒂法", "不知火舞",
    "女仆", "旗袍", "制服", "水手服", "lo裙", "lolita",
    "擦边", "福利", "涩", "绝对领域",
    "美女", "小姐姐", "热裤", "吊带", "露背",
    "兔女郎", "猫耳", "女帝", "魅魔",
    "插画", "画师", "立绘",
    "腿", "腰", "锁骨",
]

# 高权重关键词 — 权重3（命中这些几乎一定是目标内容）
HIGH_WEIGHT_KEYWORDS = [
    "cos", "cosplay", "coser",
    "二次元", "原神", "崩坏", "碧蓝", "明日方舟",
    "写真", "私房", "擦边", "福利",
    "兔女郎", "女仆", "黑丝", "白丝",
    "泳装", "比基尼",
]

# 负面关键词 — 命中直接排除
NEGATIVE_KEYWORDS = [
    "美食", "菜谱", "做饭", "烘焙", "食谱",
    "装修", "家居", "家装", "房子",
    "考研", "考公", "考试", "雅思", "托福", "四六级",
    "理财", "基金", "股票", "保险",
    "减肥", "健身计划", "增肌",
    "母婴", "宝宝", "育儿", "带娃",
    "租房", "买房", "楼盘",
    "汽车", "提车", "驾照",
    "男生穿搭", "男装",
    "招聘", "简历", "面试经验",
    "脱毛", "医美", "整形", "激光", "玻尿酸", "水光针",
    "祛痘", "祛斑", "美白针", "瘦脸针",
    "种草", "测评", "开箱", "好物分享",
    "广告", "推广", "探店", "团购", "优惠券",
    "净肤", "护肤", "面膜", "精华液",
    "鸣潮",
]

client = httpx.Client(base_url=API_BASE, timeout=TIMEOUT)


# ══════════════════════════════════════════════════════════════════════
#  Camoufox API 封装
# ══════════════════════════════════════════════════════════════════════

def api_goto(url: str):
    r = client.post("/api/goto", json={"url": url})
    r.raise_for_status()
    return r.json()


def api_click(selector: str, force: bool = False):
    try:
        # Playwright locator.click() can hang on XHS detail-page engagement buttons.
        # Use a short per-click timeout and print response bodies before raising so
        # future 500s include FastAPI's {"detail": ...} text in stderr.
        r = client.post(
            "/api/click",
            json={"selector": selector, "force": force},
            timeout=CLICK_TIMEOUT,
        )
        if r.status_code >= 400:
            print(
                f"  click HTTP {r.status_code} for '{selector}': {r.text[:1000]}",
                file=sys.stderr,
            )
        r.raise_for_status()
        return r.json()
    except Exception as e:
        print(f"  click failed for '{selector}': {e}", file=sys.stderr)
        return None


def api_js_click(selector: str):
    """Click an element inside the page with JS events.

    XHS' main like/collect controls are visible span elements. Camoufox/Playwright
    /api/click sometimes times out or returns 500 on these spans even though direct
    DOM MouseEvents work and update the count/icon. This fallback also avoids
    accidentally hitting comment like buttons by requiring the exact selector.
    """
    expression = f"""
    (() => {{
      const selector = {json.dumps(selector)};
      const el = document.querySelector(selector);
      if (!el) return {{status: 'missing', selector}};
      const rect = el.getBoundingClientRect();
      const before = {{
        className: String(el.className || ''),
        text: el.innerText || '',
        useHref: el.querySelector('use')?.getAttribute('xlink:href') || ''
      }};
      el.scrollIntoView({{block: 'center', inline: 'center'}});
      ['mouseover', 'mousedown', 'mouseup', 'click'].forEach(type => {{
        el.dispatchEvent(new MouseEvent(type, {{
          bubbles: true,
          cancelable: true,
          view: window,
          clientX: rect.left + rect.width / 2,
          clientY: rect.top + rect.height / 2
        }}));
      }});
      const after = {{
        className: String(el.className || ''),
        text: el.innerText || '',
        useHref: el.querySelector('use')?.getAttribute('xlink:href') || ''
      }};
      return {{status: 'success', selector, before, after}};
    }})()
    """
    r = api_evaluate(expression)
    if not r or r.get("status") != "success":
        print(f"  JS click failed for '{selector}': {r}", file=sys.stderr)
        return None
    result = r.get("result") or {}
    if result.get("status") != "success":
        print(f"  JS click failed for '{selector}': {result}", file=sys.stderr)
        return None
    return result


def click_engage(selector: str):
    """Click a like/collect control, falling back to JS when /api/click fails."""
    r = api_click(selector, force=True)
    if r and r.get("status") == "success":
        return {"status": "success", "method": "api", "raw": r}
    js = api_js_click(selector)
    if js and js.get("status") == "success":
        return {"status": "success", "method": "js", "raw": js}
    return None


def get_engage_state(selector: str):
    """Return visible state for a main engagement button."""
    expression = f"""
    (() => {{
      const selector = {json.dumps(selector)};
      const el = document.querySelector(selector);
      if (!el) return {{exists: false, selector}};
      const rect = el.getBoundingClientRect();
      return {{
        exists: true,
        selector,
        className: String(el.className || ''),
        text: el.innerText || '',
        visible: !!(rect.width && rect.height),
        useHref: el.querySelector('use')?.getAttribute('xlink:href') || '',
        html: el.outerHTML.slice(0, 500)
      }};
    }})()
    """
    r = api_evaluate(expression)
    if not r or r.get("status") != "success":
        return {"exists": False, "selector": selector, "error": r}
    return r.get("result") or {"exists": False, "selector": selector}


def is_liked_state(state: dict) -> bool:
    return "like-active" in (state.get("className") or "")


def is_collected_state(state: dict) -> bool:
    return "#collected" in (state.get("useHref") or "") or "collect-active" in (state.get("className") or "")


def ensure_engage(selector: str, is_active_fn, label: str):
    """Ensure like/collect is active without toggling an already-active button."""
    before = get_engage_state(selector)
    if not before.get("exists"):
        print(f"  {label}按钮不存在: {before}", file=sys.stderr)
        return False
    if is_active_fn(before):
        print(f"  {label}已是选中状态，跳过点击", file=sys.stderr)
        return True

    clicked = click_engage(selector)
    if not clicked:
        return False
    wait(1)

    after = get_engage_state(selector)
    ok = is_active_fn(after)
    print(
        f"  {label}点击({clicked.get('method')}): "
        f"before={before.get('className')}/{before.get('useHref')}/{before.get('text')} "
        f"after={after.get('className')}/{after.get('useHref')}/{after.get('text')} ok={ok}",
        file=sys.stderr,
    )
    return ok


def api_evaluate(expression: str):
    """在当前页面执行 JavaScript 表达式"""
    try:
        r = client.post("/api/evaluate", json={"expression": expression})
        r.raise_for_status()
        return r.json()
    except Exception as e:
        print(f"  evaluate failed: {e}", file=sys.stderr)
        return None


def api_scroll(direction="down", distance=500):
    r = client.post("/api/scroll", json={"direction": direction, "distance": distance})
    r.raise_for_status()
    return r.json()


def api_html():
    r = client.get("/api/html")
    r.raise_for_status()
    return r.json().get("html", "")


# ══════════════════════════════════════════════════════════════════════
#  通用工具
# ══════════════════════════════════════════════════════════════════════

def wait(sec=1.5):
    time.sleep(sec)


def parse_likes(s: str) -> int:
    """解析点赞数字符串，支持'万'单位。'1.2万' → 12000"""
    s = s.strip()
    if not s:
        return 0
    if "万" in s:
        return int(float(s.replace("万", "")) * 10000)
    try:
        return int(s.replace(",", ""))
    except (ValueError, TypeError):
        return 0


def extract_note_id(url_or_href: str) -> str:
    """从URL中提取24位hex帖子ID"""
    m = re.search(r"/([a-f0-9]{24})", url_or_href)
    return m.group(1) if m else ""


def build_detail_url_from_search(href: str) -> str:
    """
    从搜索结果的 href 构造独立详情页URL。
    搜索结果的路径是 /search_result/...，需要替换为 /discovery/item/
    以在独立页面打开（避免 explore 弹窗模式）。
    """
    note_id = extract_note_id(href)
    if not note_id:
        return href  # fallback
    # 保留原始 xsec_token
    xsec_match = re.search(r"xsec_token=([^\"&]+)", href)
    xsec_token = xsec_match.group(1) if xsec_match else ""
    xsec_token = xsec_token.replace("&amp;", "&")
    return f"https://www.xiaohongshu.com/discovery/item/{note_id}?xsec_token={xsec_token}&xsec_source=pc_search"


def dismiss_popups():
    """用 JS 检测并关闭各种弹窗/遮罩层"""
    js_dismiss = """
    (() => {
        let dismissed = 0;
        document.querySelectorAll('.reds-alert-footer__right, .reds-button--primary, [class*="confirm"], [class*="close"]').forEach(el => {
            if (el.offsetParent !== null) { el.click(); dismissed++; }
        });
        document.querySelectorAll('.reds-dialog-mask, .reds-modal-mask, [class*="mask"][class*="dialog"], [class*="overlay"]').forEach(el => {
            el.remove(); dismissed++;
        });
        document.querySelectorAll('.reds-dialog, .reds-modal, [class*="popup"][class*="container"]').forEach(el => {
            if (el.offsetParent !== null) { el.remove(); dismissed++; }
        });
        return dismissed;
    })()
    """
    result = api_evaluate(js_dismiss)
    if result and result.get("result", 0) > 0:
        print(f"  🧹 关闭了 {result['result']} 个弹窗/遮罩", file=sys.stderr)
        wait(0.5)
    return result


# ══════════════════════════════════════════════════════════════════════
#  推荐页模式
# ══════════════════════════════════════════════════════════════════════

def extract_feed_posts(html: str) -> list[dict]:
    """从推荐页 HTML 中提取帖子信息"""
    posts = []
    link_pattern = r'href="(/explore/([a-f0-9]+)\?xsec_token=([^"&]+)[^"]*)"'
    link_matches = re.findall(link_pattern, html)
    title_pattern = r'class="title"[^>]*><span[^>]*>([^<]+)</span>'
    titles = re.findall(title_pattern, html)

    seen_ids = set()
    for i, (full_path, note_id, xsec_token) in enumerate(link_matches):
        if note_id in seen_ids:
            continue
        seen_ids.add(note_id)

        xsec_token_clean = xsec_token.replace("&amp;", "&")
        detail_url = f"https://www.xiaohongshu.com/discovery/item/{note_id}?xsec_token={xsec_token_clean}&xsec_source=pc_feed"

        title = titles[len(posts)] if len(posts) < len(titles) else ""
        posts.append({"title": title, "url": detail_url, "note_id": note_id})

    return posts


def score_post(post: dict) -> int:
    """根据用户偏好给帖子打分，负面关键词直接返回 -1"""
    title = post.get("title", "").lower()
    tags = " ".join(post.get("tags", [])).lower()
    haystack = f"{title} {tags}"

    for kw in NEGATIVE_KEYWORDS:
        if kw.lower() in haystack:
            return -1

    score = 0
    for kw in HIGH_WEIGHT_KEYWORDS:
        if kw.lower() in haystack:
            score += 3
    high_lower = {k.lower() for k in HIGH_WEIGHT_KEYWORDS}
    for kw in PREFER_KEYWORDS:
        if kw.lower() in haystack and kw.lower() not in high_lower:
            score += 1
    return score


def filter_and_rank_posts(posts: list[dict], count: int) -> list[dict]:
    """筛选并排序帖子，返回最符合偏好的 top N（只选 score > 0 的）"""
    scored = [(score_post(p), p) for p in posts]
    scored.sort(key=lambda x: x[0], reverse=True)

    result = []
    for s, p in scored:
        if len(result) >= count:
            break
        if s > 0:
            result.append(p)
    return result


# ══════════════════════════════════════════════════════════════════════
#  关键词搜索模式（NEW）
# ══════════════════════════════════════════════════════════════════════

SEARCH_EXTRACT_JS = """
Array.from(document.querySelectorAll("section.note-item")).map((s, i) => ({
    idx: i,
    title: (s.querySelector(".title") || {}).textContent || "",
    likes: (s.querySelector(".like-wrapper .count") || {}).textContent || "",
    hasVideo: !!s.querySelector("video"),
    href: (s.querySelector("a.cover") || {}).href || ""
})).filter(x => x.href && !x.hasVideo)
"""


def search_extract_posts() -> list[dict]:
    """从搜索结果页用 JS evaluate 提取帖子列表（自动过滤视频帖）"""
    result = api_evaluate(SEARCH_EXTRACT_JS)
    if not result or not isinstance(result.get("result"), list):
        return []

    posts = []
    for item in result["result"]:
        href = item.get("href", "")
        note_id = extract_note_id(href)
        if not note_id:
            continue
        posts.append({
            "title": item.get("title", "").strip(),
            "likes": item.get("likes", "").strip(),
            "likes_count": parse_likes(item.get("likes", "")),
            "note_id": note_id,
            "href": href,
        })
    return posts


def rank_by_likes(posts: list[dict]) -> list[dict]:
    """按点赞数降序排列"""
    return sorted(posts, key=lambda p: p.get("likes_count", 0), reverse=True)


def search_mode(args) -> list[dict]:
    """
    关键词搜索模式：
    1. 导航到搜索URL
    2. 滚动加载更多（如果指定）
    3. JS evaluate 提取帖子列表
    4. 按点赞数排序，取候选
    5. 逐个帖子处理（已覆盖 filter/score/post processing）
    """
    keyword = args.keyword
    encoded = urllib.parse.quote(keyword)
    search_url = f"https://www.xiaohongshu.com/search_result?keyword={encoded}&source=web_search_result_notes"

    print(f"🔍 搜索关键词: {keyword}", file=sys.stderr)
    print(f"   导航到搜索页...", file=sys.stderr)
    api_goto(search_url)
    wait(3)
    dismiss_popups()

    # 滚动加载更多
    scroll_times = getattr(args, "scroll", 4) or 4
    print(f"   滚动 {scroll_times} 次加载更多结果...", file=sys.stderr)
    for i in range(scroll_times):
        api_scroll("down", 2000)
        wait(1.5)

    # 提取帖子列表
    posts = search_extract_posts()
    print(f"   提取到 {len(posts)} 个图片帖（已过滤视频帖）", file=sys.stderr)

    if not posts:
        print(json.dumps({"error": f"搜索 '{keyword}' 未找到图片帖", "results": []}, ensure_ascii=False))
        sys.exit(1)

    # 去重已处理的帖子（用于"再来一些"场景）
    seen_file = getattr(args, "seen", None)
    seen_ids = set()
    if seen_file:
        try:
            with open(seen_file) as f:
                for line in f:
                    line = line.strip()
                    if line:
                        seen_ids.add(line)
            print(f"   已排除 {len(seen_ids)} 个已处理帖子", file=sys.stderr)
        except FileNotFoundError:
            pass

    if seen_ids:
        posts = [p for p in posts if p["note_id"] not in seen_ids]
        print(f"   去重后剩余 {len(posts)} 个帖子", file=sys.stderr)

    # 按点赞数排序
    ranked = rank_by_likes(posts)

    # 取候选（比目标多2-3个，应对视频帖）
    candidate_count = args.count + 3
    candidates = ranked[:candidate_count]

    print(f"   候选帖子 ({len(candidates)}个):", file=sys.stderr)
    for i, p in enumerate(candidates):
        print(f"   [{i+1}] {p['title'][:40]:40s}  👍{p.get('likes','?'):>6s}  id={p['note_id']}", file=sys.stderr)

    # 逐个处理
    results = []
    processed_ids = []
    found = 0

    for post in candidates:
        if found >= args.count:
            break

        print(f"\n🎯 [{found+1}/{args.count}] 处理: {post['title'][:40]}...", file=sys.stderr)

        # 从搜索 href 构造详情页 URL
        detail_url = build_detail_url_from_search(post.get("href", ""))
        if not detail_url:
            detail_url = post.get("href", "")

        result = process_one_post({
            "title": post["title"],
            "url": detail_url,
            "note_id": post["note_id"],
        })

        if result.get("skipped"):
            print(f"  ⏭️ 跳过（视频帖），尝试下一个...", file=sys.stderr)
            continue

        results.append(result)
        processed_ids.append(post["note_id"])
        found += 1
        print(f"   ✅ 图片: {len(result['images'])}张, 点赞: {result['liked']}, 收藏: {result['collected']}", file=sys.stderr)

    # 保存已处理的帖子 ID（用于去重）
    if seen_file and processed_ids:
        try:
            existing = set()
            try:
                with open(seen_file) as f:
                    existing = {line.strip() for line in f if line.strip()}
            except FileNotFoundError:
                pass
            existing.update(processed_ids)
            with open(seen_file, "w") as f:
                for nid in sorted(existing):
                    f.write(nid + "\n")
            print(f"\n📝 已更新去重文件: {seen_file} ({len(existing)} 条记录)", file=sys.stderr)
        except Exception as e:
            print(f"   ⚠️ 更新去重文件失败: {e}", file=sys.stderr)

    if not results:
        print(json.dumps({"error": f"搜索 '{keyword}' 的候选帖子均为视频帖，无可用结果", "results": []}, ensure_ascii=False))
        sys.exit(1)

    return results


# ══════════════════════════════════════════════════════════════════════
#  图片提取（共用）
# ══════════════════════════════════════════════════════════════════════

def extract_detail_images_js() -> list[str]:
    """用 JS evaluate 从当前页面 DOM 中提取帖子图片URL（多回退策略）"""
    # 首选：note-slider-img 和 swiper-slide 中的 img
    js_code = """
    (() => {
        const urls = new Set();
        document.querySelectorAll(".note-slider-img img, .swiper-slide img").forEach(img => {
            let src = img.currentSrc || img.src || img.getAttribute("src") || img.getAttribute("data-src");
            if (!src) return;
            if (!src.includes("xhscdn.com")) return;
            if (src.includes("sns-avatar")) return;
            if (src.startsWith("data:")) return;
            src = src.replace(/&amp;/g, "&");
            urls.add(src);
        });
        if (urls.size > 0) return Array.from(urls);
        // 兜底：宽泛选择器
        Array.from(document.querySelectorAll("img")).forEach(img => {
            const src = img.src || img.getAttribute("data-src") || "";
            if (!src) return;
            if (src.includes("xhscdn") && !src.includes("avatar") && !src.includes("platform") && !src.includes("comment")) {
                urls.add(src.replace(/&amp;/g, "&"));
            }
        });
        return Array.from(urls);
    })()
    """
    result = api_evaluate(js_code)
    if result and isinstance(result.get("result"), list):
        return result["result"]
    return []


XHS_TAG_EXTRACT_JS = r"""
(() => {
    const tags = new Map();

    function decodeMaybeDoubleEncoded(value) {
        let text = String(value || '').trim();
        for (let i = 0; i < 2; i++) {
            try {
                const decoded = decodeURIComponent(text);
                if (decoded === text) break;
                text = decoded;
            } catch (_) {
                break;
            }
        }
        return text;
    }

    function normalizeTag(raw) {
        let tag = decodeMaybeDoubleEncoded(raw)
            .replace(/^#+/, '')
            .replace(/[\u200b\u200c\u200d\ufeff]/g, '')
            .replace(/\s+/g, '')
            .trim();
        tag = tag.replace(/[，。！？、,.;:：；)）\]】}》"'“”‘’]+$/g, '');
        if (!tag || tag.length > 50) return '';
        return tag;
    }

    function addTag(raw, source, href = '') {
        const tag = normalizeTag(raw);
        if (!tag) return;
        const key = tag.toLowerCase();
        if (!tags.has(key)) {
            tags.set(key, {tag, text: `#${tag}`, source, href});
        } else {
            const old = tags.get(key);
            if (!old.href && href) old.href = href;
            if (!old.source.includes(source)) old.source = `${old.source},${source}`;
        }
    }

    document.querySelectorAll('a#hash-tag, a.tag, a[href*="/search_result"][href*="keyword="]').forEach(a => {
        const text = (a.innerText || a.textContent || '').trim();
        if (text.startsWith('#')) addTag(text, 'anchor_text', a.href || '');

        try {
            const u = new URL(a.href, location.href);
            const keyword = u.searchParams.get('keyword');
            if (keyword) addTag(keyword, 'anchor_keyword', a.href || '');
        } catch (_) {}
    });

    const root = document.querySelector('.note-content, .note-text, .desc, #detail-desc, .content, main') || document.body;
    const bodyText = root ? (root.innerText || root.textContent || '') : '';
    const re = /(^|[\s\n])#([^\s#，。！？、,.;:：；()（）\[\]【】{}<>《》"'“”‘’]+)/gu;
    let match;
    while ((match = re.exec(bodyText)) !== null) {
        addTag(match[2], 'body_text', '');
    }

    return Array.from(tags.values());
})()
"""


def extract_detail_tags_js() -> list[dict]:
    """从当前小红书详情页提取 #话题标签。"""
    result = api_evaluate(XHS_TAG_EXTRACT_JS)
    if result and isinstance(result.get("result"), list):
        return result["result"]
    return []


def normalize_tag_for_match(tag: str) -> str:
    return re.sub(r"^#+", "", str(tag or "")).strip().lower()


def match_note_tags(tags: list[dict]) -> dict:
    """用现有正向/高权重/负向关键词匹配详情页 tags。"""
    tag_names = [normalize_tag_for_match(t.get("tag", "")) for t in tags if isinstance(t, dict)]
    tag_names = [t for t in tag_names if t]

    def matched_keywords(keywords: list[str]) -> list[str]:
        hits = []
        for kw in keywords:
            k = kw.lower().strip()
            if not k:
                continue
            # 允许 cos 命中 cosplay/cos摄影，允许 二次元 命中 二次元摄影；
            # 但不反向用短 tag 命中更长关键词，避免 #cos 误命中 coser。
            if any(k in tag for tag in tag_names):
                hits.append(kw)
        return sorted(set(hits))

    high = matched_keywords(HIGH_WEIGHT_KEYWORDS)
    positive = matched_keywords(PREFER_KEYWORDS)
    negative = matched_keywords(NEGATIVE_KEYWORDS)
    return {
        "positive": positive,
        "high_weight": high,
        "negative": negative,
        "score_bonus": len([k for k in positive if k not in high]) + len(high) * 3,
        "excluded": bool(negative),
    }


# ══════════════════════════════════════════════════════════════════════
#  帖子处理（共用）
# ══════════════════════════════════════════════════════════════════════

def process_one_post(post: dict) -> dict:
    """
    处理单个帖子：打开 → 检测视频 → 点赞 → 收藏 → 提取图片
    返回 {"title": ..., "url": ..., "images": [...], "liked": bool, "collected": bool}
    """
    result = {
        "title": post.get("title", "未知"),
        "url": post.get("url", ""),
        "note_id": post.get("note_id", ""),
        "tags": [],
        "tag_matches": {},
        "images": [],
        "liked": False,
        "collected": False,
    }

    try:
        # 1. 打开帖子详情页
        api_goto(post["url"])
        wait(4)

        dismiss_popups()

        # 2. 提取详情页 #话题标签，用于正向/反向 tag 命中
        tag_items = extract_detail_tags_js()
        tag_names = [t.get("tag", "") for t in tag_items if isinstance(t, dict) and t.get("tag")]
        tag_matches = match_note_tags(tag_items)
        result["tags"] = tag_names
        result["tag_items"] = tag_items
        result["tag_matches"] = tag_matches
        if tag_names:
            print(f"  🏷️ Tags: {' '.join('#' + t for t in tag_names)}", file=sys.stderr)
        if tag_matches.get("positive") or tag_matches.get("high_weight") or tag_matches.get("negative"):
            print(
                f"  🧭 Tag命中: high={tag_matches.get('high_weight')} "
                f"positive={tag_matches.get('positive')} negative={tag_matches.get('negative')}",
                file=sys.stderr,
            )
        if tag_matches.get("excluded"):
            print(f"  ⏭️ 命中负向tag，跳过", file=sys.stderr)
            result["skipped"] = True
            result["skip_reason"] = "negative_tag"
            return result

        # 3. 检测视频帖
        is_video = api_evaluate("!!document.querySelector(\"video, .player-container, [class*=\\\"video-player\\\"]\")")
        if is_video and is_video.get("result"):
            print(f"  ⏭️ 视频帖，跳过", file=sys.stderr)
            result["skipped"] = True
            result["skip_reason"] = "video"
            return result

        # 4. 点赞（避免重复点击已点赞状态；/api/click 失败时回退到 DOM MouseEvents）
        result["liked"] = ensure_engage(
            ".engage-bar-style .like-wrapper",
            is_liked_state,
            "点赞",
        )

        # 5. 收藏（避免重复点击已收藏状态；/api/click 失败时回退到 DOM MouseEvents）
        result["collected"] = ensure_engage(
            ".engage-bar-style .collect-wrapper",
            is_collected_state,
            "收藏",
        )

        # 6. 提取图片
        images = extract_detail_images_js()
        result["images"] = images

    except Exception as e:
        print(f"  ❌ 处理帖子失败: {e}", file=sys.stderr)

    return result


# ══════════════════════════════════════════════════════════════════════
#  Gallery
# ══════════════════════════════════════════════════════════════════════

def push_to_gallery(results: list[dict], keyword: str = ""):
    """将结果推送到 Gallery 服务"""
    all_images = []
    for r in results:
        all_images.extend(r.get("images", []))
    if not all_images:
        return
    titles = [r.get("title", "") for r in results if r.get("title")]
    if keyword:
        gallery_title = f"🔍 {keyword}: " + " / ".join(titles[:3])
    else:
        gallery_title = " / ".join(titles[:3]) if titles else "小红书涩图"
    payload = {
        "title": gallery_title,
        "images": all_images,
        "meta": {"source": "xhs", "count": len(results), "keyword": keyword},
    }
    try:
        r = httpx.post(GALLERY_API, json=payload, timeout=5)
        if r.status_code == 200:
            print(f"📤 已推送 {len(all_images)} 张图片到 Gallery", file=sys.stderr)
        else:
            print(f"⚠️ Gallery 推送失败: {r.status_code}", file=sys.stderr)
    except Exception as e:
        print(f"⚠️ Gallery 推送失败: {e}", file=sys.stderr)


# ══════════════════════════════════════════════════════════════════════
#  Main
# ══════════════════════════════════════════════════════════════════════

def main():
    parser = argparse.ArgumentParser(
        description="小红书涩图一键操作 — 支持推荐页和关键词搜索两种模式"
    )
    parser.add_argument("--count", "-c", type=int, default=1, help="操作帖子数量（推荐页默认1，搜索模式默认5）")
    parser.add_argument("--scroll", "-s", type=int, default=None, help="滚动次数（推荐页默认2，搜索模式默认4）")
    parser.add_argument("--keyword", "-k", type=str, default=None, help="搜索关键词（启用关键词搜索模式）")
    parser.add_argument("--seen", type=str, default=None, help="去重文件路径，记录已处理帖子ID（用于'再来一些'）")
    args = parser.parse_args()

    # 搜索模式默认值覆盖
    if args.keyword:
        if args.count == 1:  # argparse default, user didn't specify
            args.count = 5
        if args.scroll is None:
            args.scroll = 4
    else:
        if args.scroll is None:
            args.scroll = 2

    count = args.count

    # ── 搜索模式 ──
    if args.keyword:
        results = search_mode(args)
        push_to_gallery(results, keyword=args.keyword)
        output = {
            "mode": "search",
            "keyword": args.keyword,
            "count": len(results),
            "results": results,
        }
        print(json.dumps(output, ensure_ascii=False, indent=2))
        return

    # ── 推荐页模式 ──
    print(f"📖 打开小红书推荐页...", file=sys.stderr)
    api_goto("https://www.xiaohongshu.com/explore?channel_id=homefeed_recommend")
    wait(3)

    dismiss_popups()
    wait(1)
    dismiss_popups()
    wait(1)

    for i in range(args.scroll):
        api_scroll("down", 800)
        wait(1)

    print(f"🔍 提取帖子列表...", file=sys.stderr)
    html = api_html()
    posts = extract_feed_posts(html)
    print(f"   找到 {len(posts)} 个帖子", file=sys.stderr)

    if not posts:
        print(json.dumps({"error": "未找到帖子", "results": []}, ensure_ascii=False))
        sys.exit(1)

    selected = filter_and_rank_posts(posts, count)

    max_retry = 5
    retry = 0
    while len(selected) < count and retry < max_retry:
        retry += 1
        print(f"   ⚠️ 只筛到 {len(selected)}/{count} 个合适帖子，第{retry}次追加滚动加载...", file=sys.stderr)
        for _ in range(3):
            api_scroll("down", 1000)
            wait(1.5)
        html = api_html()
        posts = extract_feed_posts(html)
        print(f"   现在共 {len(posts)} 个帖子", file=sys.stderr)
        selected = filter_and_rank_posts(posts, count)

    if not selected:
        print(f"   ❌ 滚动 {max_retry} 轮后仍未找到符合偏好的帖子", file=sys.stderr)
        print(json.dumps({"error": "未找到符合偏好的帖子，推荐页没有相关内容", "results": []}, ensure_ascii=False))
        sys.exit(1)

    print(f"   筛选出 {len(selected)} 个目标帖子:", file=sys.stderr)
    for i, p in enumerate(selected):
        print(f"   [{i+1}] {p['title'][:40]} (score: {score_post(p)})", file=sys.stderr)

    # 逐个处理
    results = []
    all_scored = [(score_post(p), p) for p in posts if score_post(p) > 0]
    all_scored.sort(key=lambda x: x[0], reverse=True)
    selected_ids = {p["note_id"] for p in selected}
    candidate_queue = [p for _, p in all_scored if p["note_id"] not in selected_ids]

    pending = list(selected)
    processed_count = 0
    while pending and processed_count < count:
        post = pending.pop(0)
        processed_count_display = processed_count + 1
        print(f"\n🎯 [{processed_count_display}/{count}] 处理: {post['title'][:40]}...", file=sys.stderr)
        result = process_one_post(post)

        if result.get("skipped"):
            if candidate_queue:
                next_post = candidate_queue.pop(0)
                pending.insert(0, next_post)
                print(f"  ↩️ 替补: {next_post['title'][:40]}", file=sys.stderr)
            continue

        results.append(result)
        processed_count += 1
        print(f"   ✅ 图片: {len(result['images'])}张, 点赞: {result['liked']}, 收藏: {result['collected']}", file=sys.stderr)

    push_to_gallery(results)

    output = {
        "mode": "feed",
        "count": len(results),
        "results": results,
    }
    print(json.dumps(output, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()