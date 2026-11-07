import asyncio
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Protocol

from playwright.async_api import Browser, BrowserContext


@dataclass(frozen=True, slots=True)
class PageData:
    url: str
    title: str
    text: str
    truncated: bool
    observed_at: datetime


class Extractor(Protocol):
    async def extract(self, url: str) -> PageData: ...


async def finish[T](task: asyncio.Task[T]) -> tuple[T, bool]:
    cancelled = False
    while not task.done():
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError:
            cancelled = True
    return task.result(), cancelled


@asynccontextmanager
async def browser_context(browser: Browser) -> AsyncGenerator[BrowserContext]:
    acquisition = asyncio.create_task(browser.new_context())
    try:
        context = await asyncio.shield(acquisition)
    except asyncio.CancelledError:
        context, _ = await finish(acquisition)
        await finish(asyncio.create_task(context.close()))
        raise
    try:
        yield context
    finally:
        _, cancelled = await finish(asyncio.create_task(context.close()))
        if cancelled:
            raise asyncio.CancelledError


class PageExtractor:
    def __init__(self, browser: Browser) -> None:
        self.browser = browser

    async def extract(self, url: str) -> PageData:
        async with browser_context(self.browser) as context:
            page = await context.new_page()
            response = await page.goto(
                url, wait_until="domcontentloaded", timeout=60_000
            )
            if response is None:
                raise ValueError("Page returned no HTTP response")
            if response.status >= 400:
                raise ValueError(f"Page returned HTTP {response.status}")
            title = await page.title()
            text = await page.locator("body").inner_text(timeout=60_000)
            final_url = page.url
            return PageData(
                url=final_url[:8192],
                title=title[:4096],
                text=text[:100_000],
                truncated=len(final_url) > 8192
                or len(title) > 4096
                or len(text) > 100_000,
                observed_at=datetime.now(UTC),
            )
