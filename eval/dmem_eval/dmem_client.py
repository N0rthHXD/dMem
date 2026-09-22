"""Thin HTTP client for the dMem Agent Tool API."""

from __future__ import annotations

import time
import uuid
from typing import Any

import requests

_RETRYABLE_STATUS = {429, 500, 502, 503, 504}


class DMemError(RuntimeError):
    def __init__(self, message: str, *, code: str = "", status: int = 0, retryable: bool = False) -> None:
        super().__init__(message)
        self.code = code
        self.status = status
        self.retryable = retryable


class DMemClient:
    """Calls the dMem application HTTP routes with caller identity attached."""

    def __init__(
        self,
        endpoint: str,
        *,
        caller_subject: str = "locomo-zh-eval",
        caller_client: str = "locomo-zh-eval",
        timeout: float = 120.0,
        max_retries: int = 4,
    ) -> None:
        self.endpoint = endpoint.rstrip("/")
        self.timeout = timeout
        self.max_retries = max_retries
        self._session = requests.Session()
        self._session.headers.update(
            {
                "Content-Type": "application/json",
                "X-Caller-Subject-ID": caller_subject,
                "X-Caller-Client-ID": caller_client,
            }
        )

    # -- transport ---------------------------------------------------------
    def _request(
        self,
        method: str,
        path: str,
        payload: dict[str, Any] | None = None,
        *,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        url = f"{self.endpoint}{path}"
        headers: dict[str, str] = {}
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        last_error: Exception | None = None
        for attempt in range(self.max_retries + 1):
            try:
                response = self._session.request(
                    method, url, json=payload, headers=headers, timeout=self.timeout
                )
            except requests.RequestException as exc:
                last_error = exc
                if attempt >= self.max_retries:
                    break
                time.sleep(min(2.0**attempt, 8.0))
                continue
            if response.status_code in _RETRYABLE_STATUS and attempt < self.max_retries:
                time.sleep(min(2.0**attempt, 8.0))
                continue
            return self._decode(response)
        raise DMemError(f"dMem request {method} {path} failed: {last_error}")

    @staticmethod
    def _decode(response: requests.Response) -> dict[str, Any]:
        try:
            body = response.json()
        except ValueError:
            raise DMemError(
                f"dMem returned non-JSON response ({response.status_code})",
                status=response.status_code,
            ) from None
        if response.status_code >= 400 or "error" in body:
            error = body.get("error") or {}
            raise DMemError(
                str(error.get("message") or f"dMem error {response.status_code}"),
                code=str(error.get("code") or ""),
                status=response.status_code,
                retryable=bool(error.get("retryable")),
            )
        return body

    # -- operations --------------------------------------------------------
    def capabilities(self) -> dict[str, Any]:
        return self._request("GET", "/v1/capabilities")

    def ensure_scope(
        self,
        *,
        scope_type: str,
        external_id: str,
        display_name: str,
        parent_scope_id: str | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "scope_type": scope_type,
            "external_id": external_id,
            "display_name": display_name,
        }
        if parent_scope_id:
            payload["parent_scope_id"] = parent_scope_id
        body = self._request(
            "POST", "/v1/scopes/ensure", payload, idempotency_key=str(uuid.uuid4())
        )
        scope = body.get("scope") or {}
        scope_id = scope.get("scope_id") or scope.get("space_id")
        if not scope_id:
            raise DMemError("scope.ensure response did not contain a scope id")
        return scope

    def commit(
        self,
        *,
        scope_id: str,
        source: dict[str, Any],
        memories: list[dict[str, Any]],
        entities: list[dict[str, Any]] | None = None,
        facts: list[dict[str, Any]] | None = None,
        relations: list[dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "scope_id": scope_id,
            "source": source,
            "memories": memories,
        }
        if entities:
            payload["entities"] = entities
        if facts:
            payload["facts"] = facts
        if relations:
            payload["relations"] = relations
        return self._request(
            "POST", "/v1/memory-commits", payload, idempotency_key=str(uuid.uuid4())
        )

    def recall(
        self,
        *,
        query: str,
        scope_ids: list[str],
        intent: str = "auto",
        limit: int = 10,
        entities: list[str] | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "query": query,
            "scope_ids": scope_ids,
            "intent": intent,
            "limit": int(limit),
        }
        if entities:
            payload["entities"] = entities
        return self._request("POST", "/v1/recall", payload)

    def close(self) -> None:
        self._session.close()
