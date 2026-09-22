"""LoCoMo Chinese-subset evaluation harness for dMem.

The harness drives a dMem daemon over its HTTP API and runs four phases:

1. ``build``     - the configured LLM extracts structured memories from the
                   dialogue and commits them through ``POST /v1/memory-commits``
                   (Agent Tool route).
2. ``ablation``  - each retrieval-channel mode (keyword / semantic / graph
                   subsets) runs recall against the same built index by
                   restarting the daemon with a different configuration.
3. ``retrieval`` - every QA item recalls from dMem and retrieval metrics are
                   computed at memory and dialogue-turn granularity.
4. ``qa``        - the retrieved context answers the question with the
                   configured LLM and an LLM judge scores the answer.

Only ``--config`` (plus optional ``--phases``/``--run-id`` overrides) is exposed
on the command line; everything else lives in the YAML config file.
"""

__all__ = ["__version__"]

__version__ = "1.0.0"
