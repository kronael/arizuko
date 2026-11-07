import asyncio
import logging
import secrets
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Annotated

from fastapi import FastAPI, Header, HTTPException, Query, Request, Response
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from playwright.async_api import Browser, async_playwright
from pydantic import BaseModel, HttpUrl

from page import Extractor, PageData, PageExtractor

logger = logging.getLogger(__name__)
LIFETIME = 1200
CAPACITY = 128


@dataclass(frozen=True, slots=True)
class Failure:
    code: str
    message: str


@dataclass(frozen=True, slots=True)
class Entry:
    url: str
    key: str | None
    task: asyncio.Task[PageData | Failure]
    timer: asyncio.TimerHandle


class CreateExtraction(BaseModel):
    url: HttpUrl


class Tasks:
    def __init__(self, extractor: Extractor, group: asyncio.TaskGroup) -> None:
        self.extractor = extractor
        self.group = group
        self.entries: dict[str, Entry] = {}
        self.semaphore = asyncio.Semaphore(4)

    async def extract(self, url: str) -> PageData | Failure:
        try:
            async with self.semaphore:
                return await self.extractor.extract(url)
        except Exception as e:
            current = asyncio.current_task()
            request_id = current.get_name() if current is not None else "unknown"
            logger.exception("Extraction failed request_id=%s", request_id)
            return Failure("extraction_failed", str(e)[:1000])

    def create(self, url: str, key: str | None, request_id: str | None = None) -> str:
        if key is not None:
            for handle, entry in self.entries.items():
                if entry.key == key:
                    if entry.url != url:
                        raise HTTPException(
                            409,
                            detail={
                                "code": "idempotency_conflict",
                                "message": "Key belongs to another URL",
                            },
                        )
                    return handle
        if len(self.entries) >= CAPACITY:
            raise HTTPException(
                503, detail={"code": "capacity", "message": "Handle capacity reached"}
            )
        handle = secrets.token_urlsafe(24)
        task = self.group.create_task(self.extract(url), name=request_id or handle)
        timer = asyncio.get_running_loop().call_later(LIFETIME, self.close, handle)
        self.entries[handle] = Entry(url, key, task, timer)
        return handle

    def close(self, handle: str) -> asyncio.Task[PageData | Failure] | None:
        entry = self.entries.pop(handle, None)
        if entry is None:
            return None
        entry.timer.cancel()
        if not entry.task.done():
            entry.task.cancel()
        return entry.task

    async def result(self, handle: str, timeout: float) -> PageData | Failure | None:
        entry = self.entries.get(handle)
        if entry is None:
            raise HTTPException(
                404, detail={"code": "not_found", "message": "Handle not found"}
            )
        try:
            async with asyncio.timeout(timeout):
                outcome = await asyncio.shield(entry.task)
        except TimeoutError:
            outcome = None
        except asyncio.CancelledError:
            current = asyncio.current_task()
            if current is not None and current.cancelling():
                raise
            raise HTTPException(
                404, detail={"code": "not_found", "message": "Handle not found"}
            ) from None
        if self.entries.get(handle) is not entry:
            raise HTTPException(
                404, detail={"code": "not_found", "message": "Handle not found"}
            )
        return outcome


async def wait_disconnect(request: Request) -> None:
    while (await request.receive())["type"] != "http.disconnect":
        pass


@asynccontextmanager
async def lifespan(app: FastAPI):
    async with async_playwright() as playwright:
        async with await playwright.chromium.launch() as browser:
            async with asyncio.TaskGroup() as group:
                tasks = Tasks(PageExtractor(browser), group)
                app.state.tasks = tasks
                app.state.browser = browser
                try:
                    yield
                finally:
                    for handle in list(tasks.entries):
                        tasks.close(handle)


def create_app() -> FastAPI:
    app = FastAPI(lifespan=lifespan)

    @app.exception_handler(HTTPException)
    async def http_error(request: Request, exc: HTTPException) -> JSONResponse:
        del request
        return JSONResponse(status_code=exc.status_code, content=exc.detail)

    @app.exception_handler(RequestValidationError)
    async def validation_error(
        request: Request, exc: RequestValidationError
    ) -> JSONResponse:
        del request, exc
        return JSONResponse(
            status_code=422,
            content={"code": "invalid_request", "message": "Invalid request"},
        )

    @app.get("/health")
    async def health() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/ready")
    async def ready(request: Request) -> Response:
        browser: Browser = request.app.state.browser  # pyright: ignore[reportAny]
        if not browser.is_connected():
            return JSONResponse(
                status_code=503,
                content={"code": "not_ready", "message": "Browser unavailable"},
            )
        return JSONResponse(content={"status": "ok"})

    @app.post("/v1/extractions", status_code=202)
    async def create_extraction(
        body: CreateExtraction,
        request: Request,
        response: Response,
        idempotency_key: Annotated[
            str | None, Header(alias="Idempotency-Key", min_length=1, max_length=128)
        ] = None,
    ) -> dict[str, str]:
        tasks: Tasks = request.app.state.tasks  # pyright: ignore[reportAny]
        handle = tasks.create(
            str(body.url), idempotency_key, request.headers.get("X-Request-ID")
        )
        response.headers["Location"] = f"/v1/extractions/{handle}/result"
        return {"handle": handle}

    @app.get("/v1/extractions/{handle}/result", response_model=PageData)
    async def extraction_result(
        handle: str,
        request: Request,
        timeout: Annotated[float, Query(ge=0, le=1200)] = 30,
    ) -> Response | PageData:
        tasks: Tasks = request.app.state.tasks  # pyright: ignore[reportAny]
        waiter = asyncio.create_task(tasks.result(handle, timeout))
        disconnect = asyncio.create_task(wait_disconnect(request))
        try:
            done, _ = await asyncio.wait(
                {waiter, disconnect}, return_when=asyncio.FIRST_COMPLETED
            )
            if disconnect in done:
                disconnect.result()
                return Response(status_code=204)
            outcome = waiter.result()
        finally:
            waiter.cancel()
            disconnect.cancel()
            await asyncio.gather(waiter, disconnect, return_exceptions=True)
        if outcome is None:
            return Response(status_code=202)
        if isinstance(outcome, Failure):
            return JSONResponse(
                status_code=502,
                content={"code": outcome.code, "message": outcome.message},
            )
        return outcome

    @app.delete("/v1/extractions/{handle}", status_code=204)
    async def delete_extraction(handle: str, request: Request) -> Response:
        tasks: Tasks = request.app.state.tasks  # pyright: ignore[reportAny]
        task = tasks.close(handle)
        if task is not None:
            try:
                await asyncio.shield(task)
            except asyncio.CancelledError:
                current = asyncio.current_task()
                if current is not None and current.cancelling():
                    raise
        return Response(status_code=204)

    return app


app = create_app()
