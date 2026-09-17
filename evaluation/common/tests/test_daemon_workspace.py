"""Daemon workspace config rendering and path layout."""

import pytest

from dmem_eval.daemon_workspace import (
    WorkspaceError,
    WorkspacePaths,
    render_instance_config,
)


def test_render_instance_config_rewrites_paths():
    source = (
        "config_version = 1\n"
        "[instance]\n"
        'data_dir = "/old/data"\n'
        'runtime_dir = "/old/runtime"\n'
        "database_filename = \"old.db\"\n"
        "socket_filename = \"old.sock\"\n"
        "\n[server]\nrequest_timeout = \"30s\"\n"
    )
    rendered = render_instance_config(source, "/new/data", "/new/runtime")
    assert 'data_dir = "/new/data"' in rendered
    assert 'runtime_dir = "/new/runtime"' in rendered
    assert 'socket_filename = "dmem.sock"' in rendered
    assert "[server]" in rendered
    assert "request_timeout" in rendered


def test_render_instance_config_requires_instance_section():
    with pytest.raises(WorkspaceError):
        render_instance_config("config_version = 1\n", "/data", "/runtime")


def test_workspace_paths_are_private_to_the_root(tmp_path):
    paths = WorkspacePaths.under(tmp_path)
    assert paths.data_dir == tmp_path / "data"
    assert paths.runtime_dir == tmp_path / "runtime"
    assert paths.socket_path == tmp_path / "runtime" / "dmem.sock"
    assert paths.config_path == tmp_path / "dmemd.toml"
