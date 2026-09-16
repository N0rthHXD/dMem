"""dMem HTTP API client over Unix domain socket.

Talks to a running ``dmemd`` instance through its public REST API, using a
Unix-domain-socket transport provided by :mod:`httpx`. No Go ``internal``
package is imported; all request/response shapes come from the published
OpenAPI contract (``docs/api/openapi.yaml``).
"""

from __future__ import annotations

import logging
import time
from collections.abc import Callable
from typing import Any

import httpx

logger = logging.getLogger(__name__)

DEFAULT_TIMEOUT = 120.0
MAX_RETRIES = 6


class DMemError(Exception):
    """Typed dMem API error carrying HTTP status and structured error code."""

    def __init__(
        self,
        status_code: int,
        error_code: str,
        message: str,
        *,
        retryable: bool = False,
        retry_after: float | None = None,
        response_id: str | None = None,
        details: dict[str, Any] | None = None,
    ) -> None:
        self.status_code = status_code
        self.error_code = error_code
        self.message = message
        self.retryable = retryable
        self.retry_after = retry_after
        self.response_id = response_id
        self.details = details or {}
        super().__init__(f"[{status_code}] {error_code}: {message}")


class DMemClient:
    """Thin HTTP client for the subset of dMem endpoints the runner needs."""

    def __init__(
        self,
        socket_path: str,
        *,
        caller_id: str = "longmemeval",
        timeout: float = DEFAULT_TIMEOUT,
    ) -> None:
        self._socket = socket_path
        self._caller_id = caller_id
        self._timeout = timeout
        self._base_url = "http://localhost"
        self._transport = httpx.HTTPTransport(uds=socket_path)
        self._http = httpx.Client(
            transport=self._transport,
            base_url=self._base_url,
            timeout=timeout,
        )

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> DMemClient:
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    # ------------------------------------------------------------------
    # low-level transport
    # ------------------------------------------------------------------

    def _headers(self, idempotency_key: str | None = None) -> dict[str, str]:
        h: dict[str, str] = {"X-Caller-ID": self._caller_id}
        if idempotency_key:
            h["Idempotency-Key"] = idempotency_key
        return h

    def _request(
        self,
        method: str,
        path: str,
        *,
        json: Any | None = None,
        params: dict[str, Any] | None = None,
        idempotency_key: str | None = None,
        retries: int = MAX_RETRIES,
    ) -> dict[str, Any]:
        headers = self._headers(idempotency_key)
        last_exc: Exception | None = None
        for attempt in range(retries + 1):
            try:
                resp = self._http.request(
                    method, path, json=json, params=params, headers=headers
                )
            except (httpx.TransportError, httpx.TimeoutException) as exc:
                last_exc = exc
                if attempt < retries:
                    wait = min(2**attempt, 30)
                    logger.warning(
                        "transport error on %s %s: %s; retry %d/%d in %.1fs",
                        method, path, exc, attempt + 1, retries, wait,
                    )
                    time.sleep(wait)
                    continue
                raise DMemError(0, "transport_error", str(exc)) from exc

            if resp.status_code >= 400:
                try:
                    body: dict[str, Any] = resp.json()
                except Exception:
                    body = {}
                err = DMemError(
                    resp.status_code,
                    body.get("error_code", "http_error"),
                    body.get("message", resp.text),
                    retryable=body.get("retryable", False),
                    retry_after=body.get("retry_after"),
                    response_id=body.get("request_id"),
                    details=body.get("details"),
                )
                retryable_statuses = {429, 503, 502, 504}
                should_retry = (
                    err.retryable or resp.status_code in retryable_statuses
                ) and attempt < retries
                if should_retry:
                    wait = err.retry_after or min(2**attempt, 30)
                    logger.warning(
                        "retryable %s %s on %s %s; retry %d/%d in %.1fs",
                        resp.status_code, err.error_code, method, path,
                        attempt + 1, retries, wait,
                    )
                    time.sleep(wait)
                    continue
                raise err

            if resp.status_code == 204 or not resp.content:
                return {}
            return resp.json()

        raise DMemError(0, "transport_error", str(last_exc))

    # ------------------------------------------------------------------
    # health
    # ------------------------------------------------------------------

    def health(self) -> dict[str, Any]:
        """GET /v1/health — no X-Caller-ID required."""
        resp = self._http.request("GET", "/v1/health")
        if resp.status_code >= 400:
            try:
                body = resp.json()
            except Exception:
                body = {}
            raise DMemError(
                resp.status_code,
                body.get("error_code", "http_error"),
                body.get("message", resp.text),
            )
        return resp.json()

    # ------------------------------------------------------------------
    # spaces
    # ------------------------------------------------------------------

    def list_spaces(
        self,
        *,
        parent_space_id: str | None = None,
        space_type: str | None = None,
        limit: int = 200,
    ) -> list[dict[str, Any]]:
        """GET /v1/spaces — paginate, then client-side filter by space_type.

        The daemon's ListSpaces only supports ``parent_space_id`` filtering;
        ``space_type`` is not a server-side query parameter. We filter it
        client-side to avoid sending unsupported parameters.
        """
        results: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            params: dict[str, Any] = {"limit": min(limit, 200)}
            if parent_space_id:
                params["parent_space_id"] = parent_space_id
            if cursor:
                params["cursor"] = cursor
            page = self._request("GET", "/v1/spaces", params=params)
            results.extend(page.get("items", []))
            cursor = page.get("next_cursor")
            if not cursor:
                break
        if space_type:
            results = [s for s in results if s.get("space_type") == space_type]
        return results

    def create_space(
        self,
        *,
        space_type: str,
        external_id: str,
        display_name: str,
        parent_space_id: str | None = None,
        idempotency_key: str,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {
            "space_type": space_type,
            "external_id": external_id,
            "display_name": display_name,
        }
        if parent_space_id:
            body["parent_space_id"] = parent_space_id
        return self._request(
            "POST", "/v1/spaces", json=body, idempotency_key=idempotency_key
        )

    # ------------------------------------------------------------------
    # ingest / recall
    # ------------------------------------------------------------------

    def ingest_event(
        self,
        *,
        space_id: str,
        content: str,
        source_type: str = "conversation",
        source_name: str = "longmemeval",
        source_event_id: str | None = None,
        occurred_at: str,
        metadata: dict[str, Any] | None = None,
        idempotency_key: str,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {
            "space_id": space_id,
            "source_type": source_type,
            "source_name": source_name,
            "occurred_at": occurred_at,
            "content": {
                "kind": "text",
                "media_type": "text/plain",
                "text": content,
            },
        }
        if source_event_id is not None:
            body["source_event_id"] = source_event_id
        if metadata is not None:
            body["metadata"] = metadata
        return self._request(
            "POST", "/v1/events", json=body, idempotency_key=idempotency_key
        )

    def get_event(self, event_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/events/{event_id}")

    def replay_event(
        self, event_id: str, *, idempotency_key: str
    ) -> dict[str, Any]:
        """POST /v1/events/{event_id}/replay — re-run extraction for an event."""
        return self._request(
            "POST",
            f"/v1/events/{event_id}/replay",
            json={},
            idempotency_key=idempotency_key,
        )

    def recall(
        self,
        *,
        query: str,
        space_ids: list[str],
        include_ancestors: bool = False,
        reference_time: str | None = None,
        mode: str = "keyword",
        intent: str = "auto",
        limit: int = 100,
        max_context_tokens: int = 100000,
        include_evidence: bool = True,
        capabilities_override: list[str] | None = None,
        graph_policy: str | None = None,
        disable_diversification: bool = False,
    ) -> dict[str, Any]:
        capabilities = {
            "keyword": ["keyword"],
            "semantic": ["semantic"],
            "graph": ["graph"],
            "hybrid": None,
        }[mode]
        if capabilities_override is not None:
            capabilities = list(capabilities_override)
        body: dict[str, Any] = {
            "query": query,
            "space_ids": space_ids,
            "include_ancestors": include_ancestors,
            "intent": intent,
            "max_results": limit,
            "max_context_tokens": max_context_tokens,
            "include_evidence": include_evidence,
        }
        if reference_time is not None:
            body["reference_time"] = reference_time
        debug: dict[str, Any] = {}
        if capabilities is not None:
            debug["capabilities"] = capabilities
        if graph_policy is not None:
            debug["graph_policy"] = graph_policy
        if disable_diversification:
            debug["disable_diversification"] = True
        if debug:
            body["debug"] = debug
        return self._request("POST", "/v1/recall", json=body)

    # ------------------------------------------------------------------
    # index rebuilds and jobs
    # ------------------------------------------------------------------

    def rebuild_semantic(self, *, idempotency_key: str) -> dict[str, Any]:
        """POST /v1/indexes/semantic/rebuild.

        Returns ``{"operation_id": ..., "job_id": ...}`` on success.
        A 409 ``job_already_active`` is treated as success — the rebuild is
        already running and the returned ``job_id`` (from error details) can
        be polled.
        """
        try:
            return self._request(
                "POST",
                "/v1/indexes/semantic/rebuild",
                json={},
                idempotency_key=idempotency_key,
                retries=0,
            )
        except DMemError as exc:
            if exc.error_code == "job_already_active":
                return {
                    "job_id": exc.details.get("job_id", ""),
                    "operation_id": "",
                    "already_active": True,
                }
            raise

    def get_job(self, job_id: str) -> dict[str, Any]:
        return self._request("GET", f"/v1/jobs/{job_id}")

    def retry_job(
        self, job_id: str, *, idempotency_key: str
    ) -> dict[str, Any]:
        """POST /v1/jobs/{job_id}/retry for a terminal failed job."""
        return self._request(
            "POST",
            f"/v1/jobs/{job_id}/retry",
            json={},
            idempotency_key=idempotency_key,
        )

    # ------------------------------------------------------------------
    # candidates
    # ------------------------------------------------------------------

    def list_candidates(
        self,
        *,
        space_id: str,
        review_state: str | None = None,
        limit: int = 200,
    ) -> list[dict[str, Any]]:
        results: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            params: dict[str, Any] = {
                "space_id": space_id,
                "limit": min(limit, 200),
            }
            if review_state:
                params["review_state"] = review_state
            if cursor:
                params["cursor"] = cursor
            page = self._request("GET", "/v1/candidates", params=params)
            results.extend(page.get("items", []))
            cursor = page.get("next_cursor")
            if not cursor:
                return results

    def confirm_candidate(
        self,
        *,
        candidate_id: str,
        reason_code: str,
        note: str | None = None,
        idempotency_key: str,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"reason_code": reason_code}
        if note is not None:
            body["note"] = note
        return self._request(
            "POST",
            f"/v1/candidates/{candidate_id}/confirm",
            json=body,
            idempotency_key=idempotency_key,
        )

    # ------------------------------------------------------------------
    # forget
    # ------------------------------------------------------------------

    def forget(
        self,
        *,
        memory_id: str,
        reason: str = "longmemeval run reset",
        idempotency_key: str,
    ) -> dict[str, Any]:
        return self._request(
            "POST",
            f"/v1/memories/{memory_id}/forget",
            json={"reason": reason},
            idempotency_key=idempotency_key,
        )

    # ------------------------------------------------------------------
    # high-level helpers
    # ------------------------------------------------------------------

    def wait_for_semantic_ready(
        self,
        *,
        poll_interval: float = 3.0,
        timeout: float = 600.0,
        on_status: Callable[[dict[str, Any]], None] | None = None,
    ) -> dict[str, Any]:
        """Poll /v1/health until semantic_state == 'ready' and queue is empty.

        Raises immediately if semantic embedding is not configured.
        """
        first = self.health()
        if first.get("semantic_state") == "not_configured":
            raise DMemError(
                0,
                "semantic_not_configured",
                "daemon has no embedding provider configured; "
                "semantic/hybrid retrieval requires one",
            )
        deadline = time.monotonic() + timeout
        last = first
        last_state: tuple[Any, Any] | None = None
        while time.monotonic() < deadline:
            state = (last.get("semantic_state"), last.get("job_queue_depth"))
            if on_status is not None and state != last_state:
                on_status(last)
                last_state = state
            if (
                last.get("semantic_state") == "ready"
                and last.get("job_queue_depth", 1) == 0
            ):
                return last
            time.sleep(poll_interval)
            last = self.health()
        raise TimeoutError(
            f"semantic index not ready after {timeout}s; last state: "
            f"semantic={last.get('semantic_state')} "
            f"queue={last.get('job_queue_depth')}"
        )

    def wait_for_job(
        self,
        job_id: str,
        *,
        poll_interval: float = 3.0,
        timeout: float = 600.0,
        on_status: Callable[[dict[str, Any]], None] | None = None,
    ) -> dict[str, Any]:
        """Poll GET /v1/jobs/{job_id} until terminal status."""
        deadline = time.monotonic() + timeout
        last: dict[str, Any] = {}
        last_status = ""
        while time.monotonic() < deadline:
            last = self.get_job(job_id)
            status = last.get("status", "")
            if on_status is not None and status != last_status:
                on_status(last)
                last_status = status
            if status in (
                "succeeded", "failed", "cancelled", "expired",
                "blocked_policy", "superseded",
            ):
                return last
            time.sleep(poll_interval)
        raise TimeoutError(
            f"job {job_id} not terminal after {timeout}s; last status: {last.get('status')}"
        )

    def trigger_and_wait_semantic(
        self,
        *,
        poll_interval: float = 3.0,
        timeout: float = 600.0,
        idempotency_key: str,
        on_status: Callable[[dict[str, Any]], None] | None = None,
        on_job_status: Callable[[dict[str, Any]], None] | None = None,
    ) -> dict[str, Any]:
        """Trigger a rebuild, wait for it to finish, then confirm readiness."""
        result = self.rebuild_semantic(idempotency_key=idempotency_key)
        job_id = result.get("job_id") or ""
        if job_id:
            self.wait_for_job(
                job_id,
                poll_interval=poll_interval,
                timeout=timeout,
                on_status=on_job_status,
            )
        self.wait_for_semantic_ready(
            poll_interval=poll_interval,
            timeout=timeout,
            on_status=on_status,
        )
        return result
