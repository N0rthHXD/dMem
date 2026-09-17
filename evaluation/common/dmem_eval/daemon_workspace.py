"""Daemon workspace lifecycle for benchmark shell drivers.

Builds ``dmemd`` when needed, renders a workspace-local daemon TOML with the
``[instance]`` paths overridden, initializes the database, and starts/stops the
daemon while waiting for its Unix socket. The shell drivers in
``evaluation/common/`` delegate to this module so the logic is written once.
"""

from __future__ import annotations

import argparse
import os
import signal
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

DEFAULT_DATABASE_FILENAME = "dmem.db"
DEFAULT_SOCKET_FILENAME = "dmem.sock"
INSTANCE_KEYS = ("data_dir", "runtime_dir", "database_filename", "socket_filename")


class WorkspaceError(RuntimeError):
    pass


@dataclass
class WorkspacePaths:
    root: Path
    data_dir: Path
    runtime_dir: Path
    socket_path: Path
    config_path: Path
    log_path: Path
    pid_path: Path

    @classmethod
    def under(cls, root: str | Path) -> "WorkspacePaths":
        root_path = Path(root)
        return cls(
            root=root_path,
            data_dir=root_path / "data",
            runtime_dir=root_path / "runtime",
            socket_path=root_path / "runtime" / DEFAULT_SOCKET_FILENAME,
            config_path=root_path / "dmemd.toml",
            log_path=root_path / "daemon.log",
            pid_path=root_path / "daemon.pid",
        )


def build_dmemd(repo_root: str | Path, binary: str | None = None) -> Path:
    """Return an executable dmemd, building it from source when needed."""
    repo = Path(repo_root)
    target = Path(binary) if binary else repo / "dmemd"
    if not target.exists() or not os.access(target, os.X_OK):
        subprocess.run(
            ["go", "build", "-tags", "sqlite_fts5", "-o", str(target), "./cmd/dmemd"],
            cwd=repo,
            env={**os.environ, "CGO_ENABLED": "1"},
            check=True,
        )
    if not os.access(target, os.X_OK):
        raise WorkspaceError(f"dmemd not executable: {target}")
    return target


def render_instance_config(
    source_text: str, data_dir: str | Path, runtime_dir: str | Path
) -> str:
    """Rewrite the ``[instance]`` block to point at a private workspace."""
    replacements = {
        "data_dir": str(data_dir),
        "runtime_dir": str(runtime_dir),
        "database_filename": DEFAULT_DATABASE_FILENAME,
        "socket_filename": DEFAULT_SOCKET_FILENAME,
    }
    lines = source_text.splitlines()
    output: list[str] = []
    inserted = False
    for line in lines:
        output.append(line)
        if not inserted and line.strip() == "[instance]":
            for key in INSTANCE_KEYS:
                output.append(f'{key} = "{replacements[key]}"')
            inserted = True
    if not inserted:
        raise WorkspaceError("daemon config has no [instance] section")
    return "\n".join(output) + "\n"


def write_daemon_config(
    source: str | Path,
    dest: str | Path,
    paths: WorkspacePaths,
    *,
    create_dirs: bool,
) -> Path:
    source_path = Path(source)
    dest_path = Path(dest)
    if create_dirs:
        dest_path.parent.mkdir(parents=True, exist_ok=True)
        paths.data_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        paths.runtime_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    text = render_instance_config(
        source_path.read_text(encoding="utf-8"), paths.data_dir, paths.runtime_dir
    )
    dest_path.write_text(text, encoding="utf-8")
    return dest_path


def init_database(dmemd: str | Path, config: str | Path) -> None:
    subprocess.run(
        [str(dmemd), "--config", str(config), "config", "validate"],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    subprocess.run(
        [str(dmemd), "--config", str(config), "init"],
        check=True,
        stdout=subprocess.DEVNULL,
    )


def start_daemon(
    dmemd: str | Path,
    config: str | Path,
    paths: WorkspacePaths,
    *,
    init: bool,
    timeout: float = 10.0,
) -> int:
    if init:
        init_database(dmemd, config)
    log = open(paths.log_path, "ab")
    process = subprocess.Popen(
        [str(dmemd), "--config", str(config), "daemon"],
        stdout=log,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    log.close()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if paths.socket_path.exists():
            paths.pid_path.write_text(str(process.pid), encoding="utf-8")
            return process.pid
        if process.poll() is not None:
            raise WorkspaceError(
                f"daemon exited early (rc={process.returncode}); log: {paths.log_path}"
            )
        time.sleep(0.1)
    stop_daemon(paths)
    raise WorkspaceError(f"daemon did not create socket: {paths.socket_path}")


def stop_daemon(paths: WorkspacePaths, *, timeout: float = 5.0) -> None:
    if not paths.pid_path.exists():
        return
    try:
        pid = int(paths.pid_path.read_text(encoding="utf-8").strip())
    except ValueError:
        paths.pid_path.unlink(missing_ok=True)
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        paths.pid_path.unlink(missing_ok=True)
        return
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.1)
    else:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    paths.pid_path.unlink(missing_ok=True)


def _cmd_build(args: argparse.Namespace) -> int:
    binary = build_dmemd(args.repo_root, args.dmemd)
    print(binary)
    return 0


def _cmd_write_config(args: argparse.Namespace) -> int:
    paths = WorkspacePaths.under(args.workspace)
    write_daemon_config(
        args.source, paths.config_path, paths, create_dirs=not args.no_dirs
    )
    print(paths.config_path)
    return 0


def _cmd_start(args: argparse.Namespace) -> int:
    paths = WorkspacePaths.under(args.workspace)
    pid = start_daemon(
        args.dmemd, paths.config_path, paths, init=args.init, timeout=args.timeout
    )
    print(f"{pid} {paths.socket_path}")
    return 0


def _cmd_stop(args: argparse.Namespace) -> int:
    stop_daemon(WorkspacePaths.under(args.workspace))
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    build = sub.add_parser("build", help="build dmemd if needed, print path")
    build.add_argument("--repo-root", required=True)
    build.add_argument("--dmemd")
    build.set_defaults(func=_cmd_build)

    write = sub.add_parser("write-config", help="render workspace daemon TOML")
    write.add_argument("--workspace", required=True)
    write.add_argument("--source", required=True)
    write.add_argument("--no-dirs", action="store_true")
    write.set_defaults(func=_cmd_write_config)

    start = sub.add_parser("start", help="start daemon and wait for socket")
    start.add_argument("--workspace", required=True)
    start.add_argument("--dmemd", required=True)
    start.add_argument("--init", action="store_true")
    start.add_argument("--timeout", type=float, default=10.0)
    start.set_defaults(func=_cmd_start)

    stop = sub.add_parser("stop", help="stop daemon recorded in the workspace")
    stop.add_argument("--workspace", required=True)
    stop.set_defaults(func=_cmd_stop)

    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except (WorkspaceError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
