import base64
import re
from contextlib import asynccontextmanager
from typing import Any, Dict, List, Literal, Optional

from fastapi import FastAPI, HTTPException, Query
from pydantic import BaseModel, ConfigDict, Field
from camoufox.async_api import AsyncCamoufox
import uvicorn
import os

try:
    import trafilatura
except Exception:  # pragma: no cover - handled at runtime for clearer API errors
    trafilatura = None


# 定义一个绝对路径用于存放浏览器数据。
# 如果文件夹不存在，Camoufox/Playwright 会自动创建它。
PROFILE_DIR = os.path.abspath("./my_camoufox_profile")

# ================================
# 🌐 代理配置
# ================================
PROXY_SERVER = "http://127.0.0.1:25621"  # 支持 http://, https://, socks5://
PROXY_USERNAME = ""  # 如果代理不需要验证，留空即可
PROXY_PASSWORD = ""  # 如果代理不需要验证，留空即可

# 全局变量存储浏览器和页面实例
camoufox_ctx = None
context = None
page = None


# ==========================================
# Agent 友好 Helper
# ==========================================

def compact_text(text: Optional[str], limit: int = 500) -> str:
    """Normalize whitespace and truncate text for agent observations."""
    if not text:
        return ""
    text = re.sub(r"\s+", " ", text).strip()
    if len(text) <= limit:
        return text
    return text[: max(0, limit - 1)].rstrip() + "…"


def fallback_html_to_markdown(html: str) -> str:
    """Very small fallback when Trafilatura cannot extract article content."""
    text = re.sub(r"(?is)<(script|style|noscript).*?>.*?</\1>", "", html or "")
    text = re.sub(r"(?i)<br\s*/?>", "\n", text)
    text = re.sub(r"(?i)</(p|div|section|article|h[1-6]|li|tr)>", "\n", text)
    text = re.sub(r"<[^>]+>", " ", text)
    text = re.sub(r"[ \t\r\f\v]+", " ", text)
    text = re.sub(r"\n\s*\n+", "\n\n", text)
    return text.strip()


def extract_markdown_from_html(
    html: str,
    url: Optional[str] = None,
    include_fallback: bool = True,
) -> Dict[str, Any]:
    """Extract readable Markdown from HTML using Trafilatura by default."""
    if not html:
        return {
            "status": "error",
            "backend": "trafilatura",
            "url": url,
            "markdown": "",
            "length": 0,
            "message": "HTML is empty",
        }

    markdown = None
    backend = "trafilatura"
    fallback_used = False
    if trafilatura is not None:
        markdown = trafilatura.extract(
            html,
            url=url,
            output_format="markdown",
            include_comments=False,
            include_tables=True,
            include_links=True,
            favor_precision=False,
        )
    else:
        backend = "fallback"

    if not markdown and include_fallback:
        fallback_used = True
        markdown = fallback_html_to_markdown(html)

    markdown = markdown or ""
    return {
        "status": "success" if markdown else "error",
        "backend": backend,
        "fallback_used": fallback_used,
        "url": url,
        "markdown": markdown,
        "length": len(markdown),
        "message": "ok" if markdown else "No readable content extracted",
    }


def infer_role(element: Dict[str, Any]) -> str:
    role = compact_text(element.get("role"), 80)
    tag = compact_text(element.get("tag"), 40).lower()
    input_type = compact_text(element.get("type"), 40).lower()
    if role:
        return role
    if tag == "a":
        return "link"
    if tag == "button":
        return "button"
    if tag == "textarea":
        return "textbox"
    if tag == "select":
        return "combobox"
    if tag == "input":
        if input_type in {"button", "submit", "reset"}:
            return "button"
        if input_type in {"checkbox", "radio"}:
            return input_type
        return "textbox"
    return tag or "element"


def normalize_interactive_elements(elements: List[Dict[str, Any]], limit: int = 80) -> List[Dict[str, Any]]:
    """Return a compact, stable shape for LLM/Agent consumption."""
    normalized: List[Dict[str, Any]] = []
    for raw in elements[: max(0, limit)]:
        label = compact_text(
            raw.get("text")
            or raw.get("ariaLabel")
            or raw.get("placeholder")
            or raw.get("title")
            or raw.get("name")
            or raw.get("value"),
            180,
        )
        item = {
            "index": len(normalized),
            "role": infer_role(raw),
            "tag": compact_text(raw.get("tag"), 40).lower(),
            "label": label,
            "selector": compact_text(raw.get("selector"), 300),
        }
        href = compact_text(raw.get("href"), 500)
        placeholder = compact_text(raw.get("placeholder"), 180)
        if href:
            item["href"] = href
        if placeholder:
            item["placeholder"] = placeholder
        normalized.append(item)
    return normalized


def ensure_page():
    if page is None:
        raise HTTPException(status_code=503, detail="Camoufox page is not initialized")
    return page


INTERACTIVE_ELEMENTS_JS = r"""
(() => {
  function cssEscape(value) {
    if (window.CSS && CSS.escape) return CSS.escape(value);
    return String(value).replace(/[^a-zA-Z0-9_-]/g, s => '\\' + s);
  }
  function selectorFor(el) {
    if (!el || !el.tagName) return '';
    if (el.id) return '#' + cssEscape(el.id);
    const parts = [];
    let node = el;
    while (node && node.nodeType === Node.ELEMENT_NODE && parts.length < 5) {
      let part = node.tagName.toLowerCase();
      const parent = node.parentElement;
      if (!parent) {
        parts.unshift(part);
        break;
      }
      const sameTag = Array.from(parent.children).filter(c => c.tagName === node.tagName);
      if (sameTag.length > 1) part += `:nth-of-type(${sameTag.indexOf(node) + 1})`;
      parts.unshift(part);
      node = parent;
    }
    return parts.join(' > ');
  }
  function visible(el) {
    const rect = el.getBoundingClientRect();
    const style = window.getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
  }
  const query = [
    'a[href]', 'button', 'input', 'textarea', 'select',
    '[role="button"]', '[role="link"]', '[role="textbox"]', '[role="combobox"]',
    '[contenteditable="true"]', '[onclick]', '[tabindex]:not([tabindex="-1"])'
  ].join(',');
  return Array.from(document.querySelectorAll(query))
    .filter(visible)
    .slice(0, 250)
    .map(el => ({
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute('role') || '',
      text: (el.innerText || el.textContent || '').trim(),
      ariaLabel: el.getAttribute('aria-label') || '',
      title: el.getAttribute('title') || '',
      name: el.getAttribute('name') || '',
      type: el.getAttribute('type') || '',
      value: el.value || '',
      placeholder: el.getAttribute('placeholder') || '',
      href: el.href || '',
      selector: selectorFor(el)
    }));
})()
"""

PAGE_STATE_JS = r"""
(() => ({
  title: document.title,
  url: location.href,
  visible_text: document.body ? document.body.innerText : '',
  viewport: {width: window.innerWidth, height: window.innerHeight},
  scroll: {
    x: window.scrollX,
    y: window.scrollY,
    height: document.documentElement.scrollHeight,
    width: document.documentElement.scrollWidth
  }
}))()
"""


@asynccontextmanager
async def lifespan(app: FastAPI):
    global camoufox_ctx, context, page
    print("🚀 正在启动 Camoufox 引擎 (开启持久化与代理)...")

    proxy_config = {"server": PROXY_SERVER}
    if PROXY_USERNAME and PROXY_PASSWORD:
        proxy_config["username"] = PROXY_USERNAME
        proxy_config["password"] = PROXY_PASSWORD

    launch_options = dict(
        headless=False,
        humanize=True,
        geoip=True,
        os="windows",
        window=(1280, 720),
        args=["--no-sandbox"],
        persistent_context=True,
        user_data_dir=PROFILE_DIR,
        enable_cache=True,
        proxy=proxy_config,
    )

    camoufox_ctx = AsyncCamoufox(**launch_options)
    try:
        context = await camoufox_ctx.__aenter__()
    except Exception as e:
        if "Failed to get IP address" not in str(e):
            raise
        print("⚠️ geoip=True 获取代理公网 IP 失败，降级为 geoip=False 重试启动...")
        try:
            await camoufox_ctx.__aexit__(None, None, None)
        except Exception:
            pass
        launch_options["geoip"] = False
        camoufox_ctx = AsyncCamoufox(**launch_options)
        context = await camoufox_ctx.__aenter__()
    page = context.pages[0] if context.pages else await context.new_page()

    print(f"✅ 浏览器已启动！当前代理服务器: {PROXY_SERVER}")
    print(f"✅ 所有 Cookies 将保存在: {PROFILE_DIR}")

    yield

    print("🛑 正在保存数据并关闭浏览器...")
    await camoufox_ctx.__aexit__(None, None, None)


app = FastAPI(title="Camoufox Agent Browser API", lifespan=lifespan)


# ==========================================
# 请求体模型
# ==========================================
class EvaluateRequest(BaseModel):
    expression: str


class ActRequest(BaseModel):
    """统一浏览器动作请求（Go 侧与 Python 侧共用同一套参数与默认值）。"""

    model_config = ConfigDict(extra="forbid")

    action: str = Field(
        description=(
            "goto, click, click_text, fill, type, press, hover, select, "
            "scroll, wait, wait_selector, back, forward, reload"
        )
    )
    url: Optional[str] = None
    selector: Optional[str] = None
    text: Optional[str] = None
    key: Optional[str] = None
    value: Optional[str] = Field(default=None, description="select 选项的 value 或 label")
    wait_until: Literal["load", "domcontentloaded", "networkidle", "commit"] = "domcontentloaded"
    state: Literal["visible", "attached", "hidden", "detached"] = "visible"
    direction: Literal["down", "up", "left", "right"] = "down"
    distance: int = Field(default=500, ge=1, le=20000)
    timeout: int = Field(default=15000, ge=0, le=120000)
    delay: int = Field(default=100, ge=0, le=5000)
    force: bool = False
    clear: bool = False


# ==========================================
# Agent 友好 API
# ==========================================
@app.get("/api/health")
async def health():
    """服务健康检查：不读取页面 DOM，适合监控。"""
    return {
        "status": "success",
        "ready": page is not None,
        "profile_dir": PROFILE_DIR,
        "proxy": PROXY_SERVER,
        "trafilatura": trafilatura is not None,
    }


@app.get("/api/observe")
async def observe(
    text_limit: int = Query(default=4000, ge=0, le=50000),
    element_limit: int = Query(default=80, ge=0, le=200),
    include_markdown: bool = False,
):
    """返回 Agent 决策所需的页面状态、可见文本和可交互元素。"""
    p = ensure_page()
    try:
        state = await p.evaluate(PAGE_STATE_JS)
        raw_elements = await p.evaluate(INTERACTIVE_ELEMENTS_JS)
        result = {
            "status": "success",
            "title": state.get("title"),
            "url": state.get("url"),
            "visible_text": compact_text(state.get("visible_text"), text_limit),
            "viewport": state.get("viewport"),
            "scroll": state.get("scroll"),
            "interactive_elements": normalize_interactive_elements(raw_elements or [], element_limit),
        }
        if include_markdown:
            html = await p.content()
            result["markdown"] = extract_markdown_from_html(html, url=state.get("url"))
        return result
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/api/markdown")
async def get_markdown(include_fallback: bool = True):
    """将当前页面提取为 Markdown。默认后端：Trafilatura。"""
    p = ensure_page()
    try:
        html = await p.content()
        return extract_markdown_from_html(html, url=p.url, include_fallback=include_fallback)
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/api/act")
async def act(req: ActRequest):
    """统一动作接口，便于 Agent 以 JSON action 调用浏览器。"""
    p = ensure_page()
    action = req.action.lower().strip()
    try:
        if action == "goto":
            if not req.url:
                raise HTTPException(status_code=400, detail="url is required for goto")
            await p.goto(req.url, timeout=req.timeout, wait_until=req.wait_until)
            return {"status": "success", "action": action, "url": p.url}

        if action == "click":
            if not req.selector:
                raise HTTPException(status_code=400, detail="selector is required for click")
            await p.locator(req.selector).click(force=req.force, timeout=req.timeout)
            return {"status": "success", "action": action, "selector": req.selector}

        if action in {"click_text", "click_by_text"}:
            if not req.text:
                raise HTTPException(status_code=400, detail="text is required for click_text")
            await p.get_by_text(req.text, exact=False).first.click(timeout=req.timeout)
            return {"status": "success", "action": action, "text": req.text}

        if action in {"fill", "type"}:
            if not req.selector or req.text is None:
                raise HTTPException(status_code=400, detail="selector and text are required for fill/type")
            locator = p.locator(req.selector)
            if action == "fill":
                await locator.fill(req.text, timeout=req.timeout)
            else:
                if req.clear:
                    await locator.fill("", timeout=req.timeout)
                await locator.type(req.text, delay=req.delay, timeout=req.timeout)
            return {"status": "success", "action": action, "selector": req.selector}

        if action == "press":
            if not req.key:
                raise HTTPException(status_code=400, detail="key is required for press")
            if req.selector:
                await p.locator(req.selector).press(req.key, timeout=req.timeout)
            else:
                await p.keyboard.press(req.key)
            return {"status": "success", "action": action, "key": req.key, "selector": req.selector}

        if action == "hover":
            if not req.selector:
                raise HTTPException(status_code=400, detail="selector is required for hover")
            await p.locator(req.selector).hover(timeout=req.timeout)
            return {"status": "success", "action": action, "selector": req.selector}

        if action == "select":
            if not req.selector or req.value is None:
                raise HTTPException(status_code=400, detail="selector and value are required for select")
            locator = p.locator(req.selector)
            try:
                await locator.select_option(value=req.value, timeout=req.timeout)
            except Exception:
                await locator.select_option(label=req.value, timeout=req.timeout)
            return {"status": "success", "action": action, "selector": req.selector, "value": req.value}

        if action == "scroll":
            if req.direction == "down":
                x, y = 0, req.distance
            elif req.direction == "up":
                x, y = 0, -req.distance
            elif req.direction == "right":
                x, y = req.distance, 0
            else:
                x, y = -req.distance, 0
            await p.evaluate("([x, y]) => window.scrollBy({left: x, top: y, behavior: 'auto'})", [x, y])
            await p.wait_for_timeout(300)
            return {"status": "success", "action": action, "direction": req.direction, "distance": req.distance}

        if action == "wait":
            await p.wait_for_timeout(req.timeout)
            return {"status": "success", "action": action, "timeout": req.timeout}

        if action == "wait_selector":
            if not req.selector:
                raise HTTPException(status_code=400, detail="selector is required for wait_selector")
            await p.locator(req.selector).wait_for(state=req.state, timeout=req.timeout)
            return {"status": "success", "action": action, "selector": req.selector, "state": req.state}

        if action == "back":
            await p.go_back(wait_until=req.wait_until)
            return {"status": "success", "action": action, "url": p.url}

        if action == "forward":
            await p.go_forward(wait_until=req.wait_until)
            return {"status": "success", "action": action, "url": p.url}

        if action == "reload":
            await p.reload(wait_until=req.wait_until)
            return {"status": "success", "action": action, "url": p.url}

        raise HTTPException(status_code=400, detail=f"Unsupported action: {req.action}")
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


# ==========================================
# 只读 API：独立 GET 端点（Go 侧直接调用）
# ==========================================
@app.get("/api/html")
async def get_html():
    """获取当前页面的完整 HTML。"""
    p = ensure_page()
    try:
        content = await p.content()
        return {"status": "success", "url": p.url, "html": content}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/api/screenshot")
async def get_screenshot():
    """获取当前页面截图 Base64。"""
    p = ensure_page()
    try:
        image_bytes = await p.screenshot(type="jpeg", quality=80)
        base64_img = base64.b64encode(image_bytes).decode("utf-8")
        return {"status": "success", "image_base64": base64_img}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/api/evaluate")
async def evaluate_js(req: EvaluateRequest):
    """在当前页面执行 JavaScript 表达式并返回结果。"""
    p = ensure_page()
    try:
        result = await p.evaluate(req.expression)
        return {"status": "success", "result": result}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=58000, reload=False)
