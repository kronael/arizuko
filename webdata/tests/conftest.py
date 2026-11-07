# pyright: basic
import pytest_asyncio
from aiohttp import web


@pytest_asyncio.fixture
async def local_page():
    app = web.Application()

    async def page(_):
        return web.Response(
            text=(
                "<html><head><title>before</title><script>"
                "document.title='rendered';"
                "document.addEventListener('DOMContentLoaded', () => "
                "document.body.textContent='rendered body');"
                "</script></head><body>before</body></html>"
            ),
            content_type="text/html",
        )

    app.router.add_get("/", page)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]  # pyright: ignore[reportAttributeAccessIssue, reportOptionalMemberAccess]
    yield f"http://127.0.0.1:{port}/"
    await runner.cleanup()
