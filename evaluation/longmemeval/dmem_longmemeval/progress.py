"""Progress display with a no-op fallback when tqdm is unavailable."""

from __future__ import annotations

try:
    from tqdm.auto import tqdm
except ImportError:  # pragma: no cover - optional dependency
    class tqdm:  # type: ignore[no-redef]
        def __init__(self, iterable=None, **kwargs):
            self._iter = iterable

        def __enter__(self):
            return self

        def __exit__(self, *args):
            self.close()

        def __iter__(self):
            return iter(self._iter)

        def update(self, n=1):
            pass

        def set_postfix_str(self, s, refresh=True):
            pass

        def close(self):
            pass
