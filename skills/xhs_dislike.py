#!/usr/bin/env python3
"""
小红书"不喜欢"一键操作脚本
对当前打开的帖子：取消点赞 + 取消收藏

用法:
    python3 xhs_dislike.py                          # 取消当前帖子的点赞和收藏
    python3 xhs_dislike.py --keyword "明日方舟"      # 同时将关键词加入负面列表

输出: JSON 格式，包含操作结果
"""

import argparse
import json
import os
import re
import sys
import time

import httpx

API_BASE = "http://127.0.0.1:58000"
TIMEOUT = 30.0
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
MEMORY_FILE = os.path.join(PROJECT_ROOT, "data", "memory", "xhs.md")
SETU_SCRIPT = os.path.join(PROJECT_ROOT, "skills", "xhs_setu.py")

client = httpx.Client(base_url=API_BASE, timeout=TIMEOUT)


def api_evaluate(expression: str):
    try:
        r = client.post("/api/evaluate", json={"expression": expression})
        r.raise_for_status()
        return r.json()
    except Exception as e:
        print(f"  evaluate failed: {e}", file=sys.stderr)
        return None


def api_click(selector: str, force: bool = False):
    try:
        r = client.post("/api/click", json={"selector": selector, "force": force})
        r.raise_for_status()
        return r.json()
    except Exception as e:
        print(f"  click failed for '{selector}': {e}", file=sys.stderr)
        return None


def api_html():
    r = client.get("/api/html")
    r.raise_for_status()
    return r.json().get("html", "")


def wait(sec=1.0):
    time.sleep(sec)


def get_current_post_info() -> dict:
    """从当前页面提取帖子标题和URL"""
    info = {"title": "", "url": ""}

    # 获取当前 URL
    url_result = api_evaluate("window.location.href")
    if url_result and url_result.get("result"):
        info["url"] = url_result["result"]

    # 获取标题
    title_result = api_evaluate("""
        (() => {
            const el = document.querySelector('#detail-title, .title, [class*="note-title"]');
            return el ? el.textContent.trim() : document.title;
        })()
    """)
    if title_result and title_result.get("result"):
        info["title"] = title_result["result"]

    return info


def check_liked() -> bool:
    """检查当前帖子是否已点赞"""
    result = api_evaluate("""
        (() => {
            const likeBtn = document.querySelector('.engage-bar-style .like-wrapper');
            if (!likeBtn) return false;
            // 已点赞时通常有 active/liked class 或特定颜色
            return likeBtn.classList.contains('active') ||
                   likeBtn.classList.contains('liked') ||
                   likeBtn.querySelector('.like-active, [class*="active"]') !== null ||
                   likeBtn.querySelector('svg.active, [color="red"]') !== null;
        })()
    """)
    # 即使检测不确定，也尝试取消
    return True


def check_collected() -> bool:
    """检查当前帖子是否已收藏"""
    result = api_evaluate("""
        (() => {
            const btn = document.querySelector('.engage-bar-style .collect-wrapper');
            if (!btn) return false;
            return btn.classList.contains('active') ||
                   btn.classList.contains('collected') ||
                   btn.querySelector('.collect-active, [class*="active"]') !== null;
        })()
    """)
    return True


def unlike():
    """取消点赞（再点一次即取消）"""
    r = api_click(".engage-bar-style .like-wrapper", force=True)
    if r and r.get("status") == "success":
        print("  👎 取消点赞", file=sys.stderr)
        return True
    print("  ⚠️ 取消点赞失败", file=sys.stderr)
    return False


def uncollect():
    """取消收藏（再点一次即取消）"""
    r = api_click(".engage-bar-style .collect-wrapper", force=True)
    if r and r.get("status") == "success":
        print("  💔 取消收藏", file=sys.stderr)
        return True
    print("  ⚠️ 取消收藏失败", file=sys.stderr)
    return False


def add_negative_keyword_to_memory(keyword: str):
    """将关键词追加到 MEMORY.md 的负面关键词列表"""
    try:
        os.makedirs(os.path.dirname(MEMORY_FILE), exist_ok=True)
        if not os.path.exists(MEMORY_FILE):
            with open(MEMORY_FILE, "w", encoding="utf-8") as f:
                f.write("# 小红书偏好\n\n### 用户不喜欢的内容（负面关键词）\n- \n")

        with open(MEMORY_FILE, "r", encoding="utf-8") as f:
            content = f.read()

        # 检查是否已存在
        if keyword.lower() in content.lower():
            print(f"  ℹ️ 关键词「{keyword}」已在记忆中", file=sys.stderr)
            return False

        # 在负面关键词行末追加
        # 找到 "### 用户不喜欢的内容（负面关键词）" 下面的列表，追加新行
        marker = "### 用户不喜欢的内容（负面关键词）"
        if marker in content:
            # 找到该节的最后一个 "- " 行，在其后追加
            lines = content.split("\n")
            insert_idx = None
            in_section = False
            for i, line in enumerate(lines):
                if marker in line:
                    in_section = True
                    continue
                if in_section:
                    if line.startswith("- "):
                        insert_idx = i
                    elif line.startswith("#"):
                        break

            if insert_idx is not None:
                # 追加到最后一个负面关键词行的末尾（用 / 分隔）
                lines[insert_idx] = lines[insert_idx].rstrip() + f" / {keyword}"
                content = "\n".join(lines)
            else:
                # 没有找到列表项，新建一行
                idx = content.index(marker) + len(marker)
                content = content[:idx] + f"\n- {keyword}" + content[idx:]

            with open(MEMORY_FILE, "w", encoding="utf-8") as f:
                f.write(content)
            print(f"  📝 「{keyword}」已加入负面关键词（记忆文件）", file=sys.stderr)
        else:
            print(f"  ⚠️ 未找到负面关键词区块，跳过", file=sys.stderr)
            return False

    except Exception as e:
        print(f"  ❌ 写入记忆文件失败: {e}", file=sys.stderr)
        return False

    return True


def add_negative_keyword_to_script(keyword: str):
    """将关键词追加到 xhs_setu.py 的 NEGATIVE_KEYWORDS 列表"""
    try:
        with open(SETU_SCRIPT, "r", encoding="utf-8") as f:
            content = f.read()

        # 检查是否已存在
        if f'"{keyword}"' in content or f"'{keyword}'" in content:
            print(f"  ℹ️ 关键词「{keyword}」已在脚本的负面列表中", file=sys.stderr)
            return False

        # 找到 NEGATIVE_KEYWORDS 列表的末尾 ']'，在其前插入
        pattern = r'(NEGATIVE_KEYWORDS\s*=\s*\[.*?)(^\])'
        match = re.search(pattern, content, re.DOTALL | re.MULTILINE)
        if match:
            # 在 ] 前面插入新关键词
            insert_pos = match.start(2)
            indent = "    "
            new_line = f'{indent}"{keyword}",\n'
            content = content[:insert_pos] + new_line + content[insert_pos:]

            with open(SETU_SCRIPT, "w", encoding="utf-8") as f:
                f.write(content)
            print(f"  🔧 「{keyword}」已加入脚本负面关键词列表", file=sys.stderr)
            return True
        else:
            print(f"  ⚠️ 未找到 NEGATIVE_KEYWORDS 列表", file=sys.stderr)
            return False

    except Exception as e:
        print(f"  ❌ 写入脚本文件失败: {e}", file=sys.stderr)
        return False


def main():
    parser = argparse.ArgumentParser(description="小红书不喜欢操作")
    parser.add_argument("--keyword", "-k", type=str, default=None,
                        help="将该关键词加入负面列表（同时更新记忆文件和脚本）")
    args = parser.parse_args()

    print("🚫 执行「不喜欢」操作...", file=sys.stderr)

    # 获取当前帖子信息
    post_info = get_current_post_info()
    print(f"  📄 帖子: {post_info['title'][:50]}", file=sys.stderr)

    # 取消点赞和收藏
    unliked = unlike()
    wait(0.5)
    uncollected = uncollect()

    # 处理负面关键词
    keyword_added = False
    if args.keyword:
        kw = args.keyword.strip()
        if kw:
            m1 = add_negative_keyword_to_memory(kw)
            m2 = add_negative_keyword_to_script(kw)
            keyword_added = m1 or m2

    result = {
        "action": "dislike",
        "post": post_info,
        "unliked": unliked,
        "uncollected": uncollected,
        "keyword": args.keyword,
        "keyword_added": keyword_added,
    }

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
