"""OpenAI-compatible chat completion with rate-limit backoff."""

from __future__ import annotations

import random
import time

from openai import APIConnectionError, APIStatusError, OpenAI, RateLimitError


def make_client(api_key: str, base_url: str | None) -> OpenAI:
    return OpenAI(api_key=api_key, base_url=base_url, max_retries=5)


def chat_with_retry(
    client: OpenAI,
    *,
    model: str,
    prompt: str,
    max_tokens: int,
    attempts: int = 12,
):
    delay = 1.0
    for attempt in range(attempts):
        try:
            return client.chat.completions.create(
                model=model,
                messages=[{"role": "user", "content": prompt}],
                n=1,
                temperature=0,
                max_tokens=max_tokens,
            )
        except (RateLimitError, APIConnectionError):
            if attempt == attempts - 1:
                raise
            time.sleep(min(60.0, delay) + random.uniform(0.0, 1.0))
            delay *= 2
        except APIStatusError as exc:
            if attempt == attempts - 1 or exc.status_code < 500:
                raise
            time.sleep(min(60.0, delay) + random.uniform(0.0, 1.0))
            delay *= 2
    raise RuntimeError("chat_with_retry exhausted attempts")
