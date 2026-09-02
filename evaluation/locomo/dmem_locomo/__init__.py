"""dMem LoCoMo evaluation runner.

A standalone benchmark runner that evaluates dMem against the LoCoMo
(snap-research/locomo) very-long-term conversational memory benchmark by
calling dMem's public HTTP API over its Unix socket. It never imports or
starts the dMem daemon; it is a pure external client.
"""

__version__ = "1.0.0"
