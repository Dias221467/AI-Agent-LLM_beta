from playwright.sync_api import sync_playwright, Error as PlaywrightError
import sys
sys.stdout.reconfigure(encoding="utf-8")

import json
from playwright.sync_api import sync_playwright

SELECTOR_INTERACTIVE = "a, button, input, textarea, select"

def safe_print(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()

def build_observation(page):
    return page.evaluate(
        """() => {
            const candidates = Array.from(document.querySelectorAll('a, button, input, textarea, select'));
            let id = 0;

            const interactive = [];
            for (const el of candidates) {
                const rect = el.getBoundingClientRect();
                if (!rect || rect.width === 0 || rect.height === 0) continue;

                const tag = el.tagName.toLowerCase();
                const role = (tag === 'input' || tag === 'textarea') ? 'input'
                          : (tag === 'button') ? 'button'
                          : (tag === 'a') ? 'link'
                          : 'control';

                const text = (el.innerText || '').trim().slice(0, 100);
                const placeholder = (el.placeholder || '').trim().slice(0, 100);
                const value = (el.value || '').toString().slice(0, 100);
                const type = el.type || null;

                interactive.push({
                    id: id++,
                    tag,
                    role,
                    text: text || placeholder || value || '',
                    type
                });

                if (interactive.length >= 80) break;
            }

            const visibleText = (document.body && document.body.innerText)
                ? document.body.innerText.slice(0, 8000)
                : '';

            return {
                url: window.location.href,
                title: document.title,
                interactive_elements: interactive,
                visible_text: visibleText
            };
        }"""
    )

def safe_build_observation(page):
    for _ in range(3):
        try:
            return build_observation(page)
        except PlaywrightError as e:
            msg = str(e)
            if "Execution context was destroyed" in msg:
                try:
                    page.wait_for_load_state("domcontentloaded", timeout=5000)
                except Exception:
                    pass
                page.wait_for_timeout(250)
                continue
            raise
    return {
        "url": page.url,
        "title": "",
        "interactive_elements": [],
        "visible_text": ""
    }

def click_by_id(page, element_id: int):
    page.evaluate(
        """(id) => {
            const candidates = Array.from(document.querySelectorAll('a, button, input, textarea, select'));
            const visible = [];
            for (const el of candidates) {
                const rect = el.getBoundingClientRect();
                if (!rect || rect.width === 0 || rect.height === 0) continue;
                visible.push(el);
                if (visible.length >= 80) break;
            }
            const el = visible[id];
            if (!el) throw new Error('Element not found');
            el.click();
        }""",
        element_id,
    )

def type_by_id(page, element_id: int, text: str):
    page.evaluate(
        """({id, value}) => {
            function isVisible(el){
                const r = el.getBoundingClientRect();
                return r && r.width > 0 && r.height > 0;
            }

            // 1) наш "видимый список" как раньше
            const candidates = Array.from(document.querySelectorAll('a, button, input, textarea, select'));
            const visible = [];
            for (const el of candidates) {
                if (!isVisible(el)) continue;
                visible.push(el);
                if (visible.length >= 80) break;
            }

            let el = visible[id];

            // 2) если выбранный элемент НЕ input/textarea — ищем лучший input сами
            const isTextInput = (x) => {
                if (!x) return false;
                const tag = (x.tagName || "").toLowerCase();
                if (tag === "textarea") return true;
                if (tag !== "input") return false;
                const t = (x.type || "").toLowerCase();
                // допускаем текстовые поля
                return ["text", "search", "", "email"].includes(t) || !t;
            };

            function scoreInput(x){
                const ph = (x.placeholder || "").toLowerCase();
                const aria = (x.getAttribute("aria-label") || "").toLowerCase();
                const name = (x.name || "").toLowerCase();
                const meta = (ph + " " + aria + " " + name);

                let s = 0;
                // hh.ru обычно: "Профессия, должность или компания"
                if (meta.includes("професс")) s += 5;
                if (meta.includes("должност")) s += 5;
                if (meta.includes("компан")) s += 4;
                if (meta.includes("поиск")) s += 3;
                if (meta.includes("search")) s += 2;

                // штрафуем логинные поля
                if (meta.includes("телефон") || meta.includes("phone")) s -= 100;
                if (meta.includes("парол") || meta.includes("password")) s -= 100;
                if (meta.includes("email")) s -= 50;

                return s;
            }

            if (!isTextInput(el)) {
                const inputs = Array.from(document.querySelectorAll('input, textarea'))
                    .filter(x => isVisible(x) && isTextInput(x));

                if (!inputs.length) throw new Error("No visible inputs found on page");

                inputs.sort((a,b) => scoreInput(b) - scoreInput(a));
                el = inputs[0];
            }

            // 3) ввод
            el.focus();
            el.value = value;
            el.dispatchEvent(new Event('input', { bubbles: true }));
        }""",
        {"id": element_id, "value": text},
    )

def scroll(page, direction: str):
    page.evaluate(
        """(dir) => {
            window.scrollBy(0, dir === 'down' ? 700 : -700);
        }""",
        direction,
    )

def wait_ms(page, ms: int):
    page.wait_for_timeout(ms)

def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False)
        context = browser.new_context()
        page = context.new_page()

        safe_print({"status": "ok", "message": "worker_started"})

        while True:
            line = sys.stdin.readline()
            if not line:
                break

            line = line.strip()
            if not line:
                continue

            try:
                cmd = json.loads(line)
                action = cmd.get("action")
                args = cmd.get("args", {})

                if action == "navigate":
                    page.goto(args["url"], wait_until="domcontentloaded")

                elif action == "click":
                    click_by_id(page, int(args["element_id"]))
                    # клик может вызвать навигацию/перерисовку
                    try:
                        page.wait_for_load_state("domcontentloaded", timeout=5000)
                    except Exception:
                        pass
                    page.wait_for_timeout(200)

                elif action == "type":
                    type_by_id(page, int(args["element_id"]), str(args["text"]))
                    page.wait_for_timeout(150)

                elif action == "scroll":
                    scroll(page, str(args.get("direction", "down")))

                elif action == "wait":
                    wait_ms(page, int(args.get("milliseconds", 500)))

                elif action == "observe":
                    pass

                elif action == "exit":
                    safe_print({"status": "ok", "message": "exiting"})
                    break

                else:
                    raise ValueError(f"Unknown action: {action}")

                obs = safe_build_observation(page)
                safe_print({"status": "ok", "observation": obs})

            except Exception as e:
                safe_print({"status": "error", "message": str(e)})

        browser.close()

if __name__ == "__main__":
    main()
