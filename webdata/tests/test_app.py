# pyright: basic
import asyncio
from datetime import UTC, datetime

import httpx
import pytest
from fastapi import HTTPException

import app
from page import PageData


class HeldExtractor:
    def __init__(self):
        self.started = asyncio.Event()
        self.release = asyncio.Event()
        self.cleanup = asyncio.Event()
        self.cleaned = asyncio.Event()
        self.calls = 0
        self.fail = False

    async def extract(self, url):
        self.calls += 1
        self.started.set()
        try:
            await self.release.wait()
            if self.fail:
                raise ValueError("failed")
            return PageData(url, "title", "body", False, datetime.now(UTC))
        finally:
            await self.cleanup.wait()
            self.cleaned.set()


@pytest.mark.asyncio
async def test_waiters_timeout_cancel_and_cached_failure():
    extractor = HeldExtractor()
    extractor.cleanup.set()
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        handle = tasks.create("https://example.com/", None)
        await extractor.started.wait()
        assert await tasks.result(handle, 0) is None
        cancelled = asyncio.create_task(tasks.result(handle, 30))
        await asyncio.sleep(0)
        cancelled.cancel()
        with pytest.raises(asyncio.CancelledError):
            await cancelled
        extractor.fail = True
        first = asyncio.create_task(tasks.result(handle, 30))
        second = asyncio.create_task(tasks.result(handle, 30))
        extractor.release.set()
        assert await first == await second == app.Failure("extraction_failed", "failed")
        assert await tasks.result(handle, 30) == await first
        assert extractor.calls == 1
        tasks.close(handle)


@pytest.mark.asyncio
async def test_close_waits_for_cleanup_and_waiter_gets_404():
    extractor = HeldExtractor()
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        handle = tasks.create("https://example.com/", None)
        await extractor.started.wait()
        waiter = asyncio.create_task(tasks.result(handle, 30))
        await asyncio.sleep(0)
        task = tasks.close(handle)
        assert task is not None
        await asyncio.sleep(0)
        assert not task.done()
        extractor.cleanup.set()
        with pytest.raises(HTTPException) as error:
            await waiter
        assert error.value.status_code == 404
        with pytest.raises(asyncio.CancelledError):
            await task
        assert extractor.cleaned.is_set()


@pytest.mark.asyncio
async def test_capacity_idempotency_and_expiry(monkeypatch):
    extractor = HeldExtractor()
    extractor.cleanup.set()
    monkeypatch.setattr(app, "CAPACITY", 2)
    monkeypatch.setattr(app, "LIFETIME", 0.01)
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        handle = tasks.create("https://example.com/", "key")
        assert tasks.create("https://example.com/", "key") == handle
        with pytest.raises(HTTPException) as error:
            tasks.create("https://other.com/", "key")
        assert error.value.status_code == 409
        tasks.create("https://other.com/", None)
        with pytest.raises(HTTPException) as error:
            tasks.create("https://third.com/", None)
        assert error.value.status_code == 503
        await asyncio.sleep(0.03)
        assert not tasks.entries
        assert tasks.create("https://other.com/", "key") != handle
        for handle in list(tasks.entries):
            tasks.close(handle)


@pytest.mark.asyncio
async def test_http_validation_and_idempotency(monkeypatch):
    extractor = HeldExtractor()
    extractor.cleanup.set()
    monkeypatch.setattr(
        app.PageExtractor, "extract", lambda _, url: extractor.extract(url)
    )
    service = app.create_app()
    async with service.router.lifespan_context(service):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=service), base_url="http://test"
        ) as client:
            assert (
                await client.post("/v1/extractions", json={"url": "file:///x"})
            ).status_code == 422
            response = await client.post(
                "/v1/extractions",
                json={"url": "https://example.com"},
                headers={"Idempotency-Key": "key"},
            )
            handle = response.json()["handle"]
            assert response.status_code == 202
            assert (
                await client.post(
                    "/v1/extractions",
                    json={"url": "https://example.com"},
                    headers={"Idempotency-Key": "key"},
                )
            ).json()["handle"] == handle
            assert (
                await client.post(
                    "/v1/extractions",
                    json={"url": "https://other.com"},
                    headers={"Idempotency-Key": "key"},
                )
            ).status_code == 409
            assert (
                await client.get(f"/v1/extractions/{handle}/result?timeout=1201")
            ).status_code == 422
            assert (
                await client.get(f"/v1/extractions/{handle}/result?timeout=0")
            ).status_code == 202
            assert (await client.delete(f"/v1/extractions/{handle}")).status_code == 204
            assert (
                await client.get(f"/v1/extractions/{handle}/result")
            ).status_code == 404
            assert (await client.delete(f"/v1/extractions/{handle}")).status_code == 204


@pytest.mark.smoke
@pytest.mark.asyncio
async def test_browser_smoke(local_page):
    service = app.create_app()
    async with service.router.lifespan_context(service):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=service), base_url="http://test"
        ) as client:
            handle = (
                await client.post("/v1/extractions", json={"url": local_page})
            ).json()["handle"]
            response = await client.get(f"/v1/extractions/{handle}/result?timeout=30")
            assert response.status_code == 200
            data = response.json()
            assert data["url"] == local_page
            assert data["title"] == "rendered"
            assert data["text"] == "rendered body"
            assert datetime.fromisoformat(
                data["observed_at"]
            ).utcoffset() == UTC.utcoffset(None)
            assert not service.state.tasks.extractor.browser.contexts
            assert (await client.delete(f"/v1/extractions/{handle}")).status_code == 204


@pytest.mark.asyncio
async def test_shutdown_drains_removed_task():
    extractor = HeldExtractor()
    closed = asyncio.Event()

    async def run_group():
        async with asyncio.TaskGroup() as group:
            tasks = app.Tasks(extractor, group)
            handle = tasks.create("https://example.com/", None)
            await extractor.started.wait()
            tasks.close(handle)
            closed.set()

    shutdown = asyncio.create_task(run_group())
    await closed.wait()
    assert not shutdown.done()
    extractor.cleanup.set()
    await shutdown
    assert extractor.cleaned.is_set()


@pytest.mark.asyncio
async def test_semaphore_holds_during_cleanup():
    extractor = HeldExtractor()
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        handles = [tasks.create(f"https://example.com/{i}", None) for i in range(5)]
        await extractor.started.wait()
        while extractor.calls < 4:
            await asyncio.sleep(0)
        assert extractor.calls == 4
        tasks.close(handles[0])
        await asyncio.sleep(0)
        assert extractor.calls == 4
        extractor.cleanup.set()
        while extractor.calls < 5:
            await asyncio.sleep(0)
        for handle in handles:
            tasks.close(handle)


@pytest.mark.asyncio
async def test_expiry_removes_completed_result(monkeypatch):
    extractor = HeldExtractor()
    extractor.release.set()
    extractor.cleanup.set()
    monkeypatch.setattr(app, "LIFETIME", 0.01)
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        handle = tasks.create("https://example.com/", None)
        assert isinstance(await tasks.result(handle, 30), PageData)
        await asyncio.sleep(0.03)
        with pytest.raises(HTTPException) as error:
            await tasks.result(handle, 30)
        assert error.value.status_code == 404


@pytest.mark.asyncio
async def test_http_readiness_request_id_and_failure(monkeypatch, caplog):
    extractor = HeldExtractor()
    extractor.release.set()
    extractor.cleanup.set()
    extractor.fail = True
    monkeypatch.setattr(
        app.PageExtractor, "extract", lambda _, url: extractor.extract(url)
    )
    service = app.create_app()
    async with service.router.lifespan_context(service):
        async with httpx.AsyncClient(
            transport=httpx.ASGITransport(app=service), base_url="http://test"
        ) as client:
            assert (await client.get("/ready")).status_code == 200
            browser = service.state.browser
            monkeypatch.setattr(browser, "is_connected", lambda: False)
            assert (await client.get("/ready")).json() == {
                "code": "not_ready",
                "message": "Browser unavailable",
            }
            assert (await client.get("/ready")).status_code == 503
            assert (await client.get("/health")).status_code == 200
            response = await client.post(
                "/v1/extractions",
                json={"url": "https://example.com"},
                headers={"X-Request-ID": "request-123"},
            )
            handle = response.json()["handle"]
            assert response.headers["location"] == f"/v1/extractions/{handle}/result"
            result = await client.get(response.headers["location"])
            assert result.status_code == 502
            assert result.json() == {
                "code": "extraction_failed",
                "message": "failed",
            }
            assert "request_id=request-123" in caplog.text
            assert (
                await client.post(
                    "/v1/extractions",
                    json={"url": "https://example.com"},
                    headers={"Idempotency-Key": "x" * 129},
                )
            ).status_code == 422


@pytest.mark.asyncio
async def test_disconnect_releases_http_waiter():
    extractor = HeldExtractor()
    extractor.cleanup.set()
    service = app.create_app()
    async with asyncio.TaskGroup() as group:
        tasks = app.Tasks(extractor, group)
        service.state.tasks = tasks
        handle = tasks.create("https://example.com/", None)
        await extractor.started.wait()
        messages = asyncio.Queue()
        await messages.put({"type": "http.request", "body": b"", "more_body": False})
        received = asyncio.Event()

        async def receive():
            message = await messages.get()
            if message["type"] == "http.disconnect":
                received.set()
            return message

        async def send(message):
            pass

        scope = {
            "type": "http",
            "asgi": {"version": "3.0"},
            "http_version": "1.1",
            "method": "GET",
            "scheme": "http",
            "path": f"/v1/extractions/{handle}/result",
            "raw_path": f"/v1/extractions/{handle}/result".encode(),
            "query_string": b"timeout=1200",
            "headers": [],
            "server": ("test", 80),
            "client": ("test", 1),
            "root_path": "",
        }
        waiter = asyncio.create_task(service(scope, receive, send))
        await messages.put({"type": "http.disconnect"})
        try:
            async with asyncio.timeout(0.3):
                await asyncio.shield(waiter)
            assert received.is_set()
            assert not tasks.entries[handle].task.done()
        finally:
            waiter.cancel()
            await asyncio.gather(waiter, return_exceptions=True)
            tasks.close(handle)


@pytest.mark.smoke
@pytest.mark.asyncio
async def test_cancel_during_context_acquisition(monkeypatch):
    service = app.create_app()
    async with service.router.lifespan_context(service):
        tasks = service.state.tasks
        browser = service.state.browser
        original = browser.new_context
        acquired = asyncio.Event()
        release = asyncio.Event()

        async def held_context():
            context = await original()
            acquired.set()
            await release.wait()
            return context

        monkeypatch.setattr(browser, "new_context", held_context)
        handle = tasks.create("https://example.com/", None)
        await acquired.wait()
        task = tasks.close(handle)
        try:
            with pytest.raises(TimeoutError):
                await asyncio.wait_for(asyncio.shield(task), 0.1)
        finally:
            release.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert not browser.contexts


@pytest.mark.smoke
@pytest.mark.asyncio
async def test_cancel_during_context_close(monkeypatch, local_page):
    service = app.create_app()
    async with service.router.lifespan_context(service):
        tasks = service.state.tasks
        browser = service.state.browser
        original = browser.new_context
        closing = asyncio.Event()
        release = asyncio.Event()

        async def held_context():
            context = await original()
            close = context.close

            async def held_close():
                closing.set()
                await release.wait()
                await close()

            monkeypatch.setattr(context, "close", held_close)
            return context

        monkeypatch.setattr(browser, "new_context", held_context)
        handle = tasks.create(local_page, None)
        await closing.wait()
        task = tasks.close(handle)
        try:
            with pytest.raises(TimeoutError):
                await asyncio.wait_for(asyncio.shield(task), 0.1)
        finally:
            release.set()
        with pytest.raises(asyncio.CancelledError):
            await task
        assert not browser.contexts
