import asyncio, sys, pathlib, threading, functools, http.server
from playwright.async_api import async_playwright
ROOT = pathlib.Path(__file__).resolve().parent.parent
DIST = ROOT / "frontend/dist"
MOCK = (ROOT / "dev/mock.js").read_text()
OUT = pathlib.Path("/workspace/work/shots"); OUT.mkdir(parents=True, exist_ok=True)
tag = sys.argv[1] if len(sys.argv) > 1 else "v"
only = sys.argv[2].split(",") if len(sys.argv) > 2 else None
H = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(DIST))
class Q(http.server.SimpleHTTPRequestHandler):
    def log_message(self,*a): pass
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Q, directory=str(DIST)))
threading.Thread(target=srv.serve_forever, daemon=True).start()
URL = f"http://127.0.0.1:{srv.server_address[1]}/index.html"
sizes = [(1440, 900), (1100, 760), (900, 720)]
async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path="/usr/bin/chromium", args=["--no-sandbox"])
        errs = []
        for w, h in sizes:
            pg = await b.new_page(viewport={"width": w, "height": h})
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: m.type == "error" and errs.append(m.text))
            await pg.add_init_script("window.__MOCK_SPEED=6;" + MOCK)
            await pg.goto(URL)
            await pg.wait_for_timeout(1500)
            async def shot(n): await pg.screenshot(path=str(OUT / f"{tag}-{n}-{w}.png"))
            await shot("studio")
            await pg.evaluate("document.querySelector('#view-studio').scrollTop=99999"); await pg.wait_for_timeout(300)
            await shot("studio-bottom")
            await pg.evaluate("document.querySelector('#view-studio').scrollTop=0")
            if w == 1440:
                await pg.click('.agent[data-agent="b"] [data-act="fetch"]')
                await pg.wait_for_timeout(1200); await shot("combo")
                await pg.keyboard.press("Escape"); await pg.mouse.click(700, 150)
            await pg.click("#btn-start")
            await pg.wait_for_timeout(1500); await shot("live")
            await pg.wait_for_timeout(9000); await shot("done")
            await pg.click('.rail-item[data-r="2"]'); await pg.wait_for_timeout(800)
            await pg.evaluate("document.querySelector('#view-arena').scrollTop=99999"); await pg.wait_for_timeout(400)
            await shot("diff")
            await pg.click('[data-go="archive"]'); await pg.wait_for_timeout(1200); await shot("archive")
            await pg.close()
        print("ERRORS:", errs)
        await b.close()
asyncio.run(main())
