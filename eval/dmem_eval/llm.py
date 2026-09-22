"""OpenAI-compatible LLM helpers with bounded concurrency and retries."""

from __future__ import annotations

import json
import os
import re
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Callable, Iterable, Sequence, TypeVar

import httpx
from openai import OpenAI

T = TypeVar("T")
R = TypeVar("R")

_JSON_FENCE = re.compile(r"```(?:json)?\s*(.*?)```", re.DOTALL)


class LLMError(RuntimeError):
    pass


class LLMClient:
    """Chat-completions client for any OpenAI-compatible endpoint."""

    def __init__(
        self,
        *,
        base_url: str,
        model: str,
        api_key_env: str,
        temperature: float = 0.0,
        max_tokens: int = 2048,
        timeout_seconds: float = 180.0,
        max_retries: int = 4,
        max_concurrent: int = 8,
    ) -> None:
        api_key = os.environ.get(api_key_env, "").strip()
        if not api_key:
            raise LLMError(
                f"environment variable {api_key_env} is not set; export the LLM/embedding API key first"
            )
        self.model = model
        self.temperature = temperature
        self.max_tokens = max_tokens
        self.max_retries = max_retries
        self.max_concurrent = max(1, int(max_concurrent))
        # Build the transport explicitly: the ambient all_proxy (socks5) is not
        # understood by httpx, while https_proxy may still be required for egress.
        proxy = (
            os.environ.get("https_proxy")
            or os.environ.get("HTTPS_PROXY")
            or os.environ.get("http_proxy")
            or os.environ.get("HTTP_PROXY")
        )
        http_client = httpx.Client(
            trust_env=False,
            proxy=proxy or None,
            timeout=timeout_seconds,
        )
        self._client = OpenAI(
            base_url=base_url, api_key=api_key, timeout=timeout_seconds, http_client=http_client
        )
        self._semaphore = threading.Semaphore(self.max_concurrent)

    def chat(
        self,
        messages: Sequence[dict[str, str]],
        *,
        json_mode: bool = False,
        temperature: float | None = None,
        max_tokens: int | None = None,
    ) -> str:
        last_error: Exception | None = None
        for attempt in range(self.max_retries + 1):
            try:
                with self._semaphore:
                    kwargs: dict[str, Any] = {
                        "model": self.model,
                        "messages": list(messages),
                        "temperature": self.temperature if temperature is None else temperature,
                        "max_tokens": self.max_tokens if max_tokens is None else max_tokens,
                    }
                    if json_mode:
                        kwargs["response_format"] = {"type": "json_object"}
                    response = self._client.chat.completions.create(**kwargs)
                content = response.choices[0].message.content or ""
                if not content.strip():
                    raise LLMError("empty completion")
                return content
            except Exception as exc:  # noqa: BLE001 - provider errors are heterogeneous
                last_error = exc
                if attempt >= self.max_retries:
                    break
                time.sleep(min(2.0**attempt, 16.0))
        raise LLMError(f"LLM request failed after {self.max_retries + 1} attempts: {last_error}")

    def chat_json(
        self,
        messages: Sequence[dict[str, str]],
        *,
        temperature: float | None = None,
        max_tokens: int | None = None,
    ) -> dict[str, Any]:
        content = self.chat(
            messages, json_mode=True, temperature=temperature, max_tokens=max_tokens
        )
        return parse_json_object(content)


def parse_json_object(content: str) -> dict[str, Any]:
    """Parse a JSON object from a model response, tolerating code fences."""

    text = content.strip()
    fence = _JSON_FENCE.search(text)
    if fence:
        text = fence.group(1).strip()
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        start, end = text.find("{"), text.rfind("}")
        if start == -1 or end <= start:
            raise LLMError(f"model response is not JSON: {content[:200]!r}") from None
        try:
            value = json.loads(text[start : end + 1])
        except json.JSONDecodeError as exc:
            raise LLMError(f"model response is not valid JSON: {exc}") from None
    if not isinstance(value, dict):
        raise LLMError("model response is not a JSON object")
    return value


def run_concurrent(
    func: Callable[[T], R],
    items: Iterable[T],
    *,
    workers: int,
    description: str = "tasks",
    progress_every: int = 25,
    log: Callable[[str], None] = print,
) -> list[R]:
    """Map ``func`` over ``items`` with a thread pool, preserving input order."""

    materialized = list(items)
    if not materialized:
        return []
    results: list[R | None] = [None] * len(materialized)
    failures: list[tuple[int, BaseException]] = []
    lock = threading.Lock()
    completed = 0

    def worker(position: int, item: T) -> None:
        nonlocal completed
        try:
            value = func(item)
        except BaseException as exc:  # noqa: BLE001 - reported after the pool drains
            with lock:
                failures.append((position, exc))
        else:
            results[position] = value
        with lock:
            completed += 1
            if progress_every and completed % progress_every == 0:
                log(f"  {description}: {completed}/{len(materialized)}")

    with ThreadPoolExecutor(max_workers=max(1, int(workers))) as pool:
        for position, item in enumerate(materialized):
            pool.submit(worker, position, item)

    if failures:
        position, error = failures[0]
        raise LLMError(
            f"{len(failures)} {description} failed; first at index {position}: {error}"
        ) from error
    return [value for value in results if value is not None]  # type: ignore[misc]
