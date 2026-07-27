"""
aiapiport CLI entry point.

Locates the Go binary and proxies all args to it.
Search order:
  1. $AIAPIPORT_BIN  (override)
  2. ~/.local/bin/aiapiport-bin  (installed alongside pip)
  3. Binary bundled in this package's data dir
  4. Offer to build from source if Go is available
"""

import os
import subprocess
import sys
import platform
from pathlib import Path


def _binary_name() -> str:
    sys_name = platform.system()
    machine = platform.machine().lower()
    if sys_name == "Windows":
        return "aiapiport-bin.exe"
    if sys_name == "Linux":
        if machine in ("x86_64", "amd64"):
            return "aiapiport-bin-linux-amd64"
        if machine in ("aarch64", "arm64"):
            return "aiapiport-bin-linux-arm64"
    # macOS (Darwin) — arm64 or x86_64, bundled as "aiapiport-bin"
    return "aiapiport-bin"


def _find_binary() -> str | None:
    # 1. Env override
    env = os.environ.get("AIAPIPORT_BIN")
    if env and Path(env).is_file():
        return env

    # 2. Same directory as this script (for editable / venv installs)
    pkg_dir = Path(__file__).parent
    candidate = pkg_dir / _binary_name()
    if candidate.is_file():
        return str(candidate)

    # 3. ~/.local/bin
    local_bin = Path.home() / ".local" / "bin" / _binary_name()
    if local_bin.is_file():
        return str(local_bin)

    return None


def _build_from_source() -> str | None:
    """Try to build the Go binary from source in the repo root."""
    # repo root is two levels up from aiapiport/__main__.py
    repo = Path(__file__).parent.parent
    if not (repo / "go.mod").is_file():
        return None

    go = "go"
    try:
        subprocess.run([go, "version"], check=True, capture_output=True)
    except (FileNotFoundError, subprocess.CalledProcessError):
        return None

    out = Path(__file__).parent / _binary_name()
    print(f"[aiapiport] Building Go binary (first run)...", file=sys.stderr)
    result = subprocess.run(
        [
            go, "build",
            "-ldflags", "-s -w",
            "-trimpath",
            "-o", str(out),
            str(repo),
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"[aiapiport] Build failed:\n{result.stderr}", file=sys.stderr)
        return None
    out.chmod(0o755)
    print(f"[aiapiport] Built {out}", file=sys.stderr)
    return str(out)


def main() -> None:
    binary = _find_binary()

    if binary is None:
        binary = _build_from_source()

    if binary is None:
        print(
            "[aiapiport] Could not find or build the aiapiport binary.\n"
            "  Option 1: set AIAPIPORT_BIN=/path/to/aiapiport-bin\n"
            "  Option 2: install Go (https://go.dev) and re-run — it will build automatically\n"
            "  Option 3: download a release binary from GitHub and put it in ~/.local/bin/aiapiport-bin",
            file=sys.stderr,
        )
        sys.exit(1)

    os.execv(binary, [binary] + sys.argv[1:])


if __name__ == "__main__":
    main()