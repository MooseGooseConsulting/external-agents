#!/usr/bin/env python3
"""Validate a GoReleaser release dist for the entire-agent-kimi-code adapter.

Checks a GoReleaser output directory (default: dist) from the repository root:
exactly the six expected archives (linux/darwin/windows x amd64/arm64), the
checksums file with correct hashes, safe archive members, exactly one
correctly named executable per archive plus README.md and CHANGELOG.md, and
that the host-platform binary runs and reports its version and valid `info`
JSON.

Stdlib only. Intended for CI: `python agents/entire-agent-kimi-code/scripts/verify-release.py --dist dist`.
"""

import argparse
import hashlib
import json
import platform
import re
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path

BINARY_BASE = "entire-agent-kimi-code"
CHECKSUMS_NAME = "checksums.txt"
ARCH_OS = ("linux", "darwin", "windows")
ARCH_ARCH = ("amd64", "arm64")
CHECKSUM_HASH = re.compile(r"^[0-9a-f]{64}$")
# GoReleaser also writes its resolved configuration, changelog, and JSON indexes at the dist root.
ROOT_EXTRA_ALLOWLIST = {"artifacts.json", "CHANGELOG.md", "config.yaml", "metadata.json"}
HOST_TIMEOUT_SECONDS = 60


def archive_name(os_name: str, arch: str) -> str:
    ext = ".zip" if os_name == "windows" else ".tar.gz"
    return f"{BINARY_BASE}_{os_name}_{arch}{ext}"


def binary_name(os_name: str) -> str:
    return f"{BINARY_BASE}.exe" if os_name == "windows" else BINARY_BASE


def fail(errors: list[str], message: str) -> None:
    errors.append(message)


def check_dist_layout(dist: Path, expected_archives: set[str], errors: list[str]) -> None:
    if not dist.is_dir():
        fail(errors, f"dist directory does not exist: {dist}")
        return
    present = {p.name for p in dist.iterdir()}
    for name in sorted(expected_archives - present):
        fail(errors, f"missing expected archive: {name}")
    unexpected = present - expected_archives - {CHECKSUMS_NAME} - ROOT_EXTRA_ALLOWLIST
    # Directory entries are GoReleaser per-build staging directories.
    unexpected = {n for n in unexpected if (dist / n).is_file()}
    for name in sorted(unexpected):
        fail(errors, f"unexpected file in dist root: {name}")


def check_checksums(dist: Path, expected_archives: set[str], errors: list[str]) -> None:
    path = dist / CHECKSUMS_NAME
    if not path.is_file():
        fail(errors, f"missing {CHECKSUMS_NAME}")
        return
    seen: dict[str, str] = {}
    for lineno, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        line = raw.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) != 2 or not CHECKSUM_HASH.match(parts[0]):
            fail(errors, f"{CHECKSUMS_NAME}:{lineno}: malformed checksum line")
            continue
        digest, name = parts
        if name in seen:
            fail(errors, f"{CHECKSUMS_NAME}:{lineno}: duplicate entry for {name}")
        seen[name] = digest
    for name in sorted(expected_archives):
        if name not in seen:
            fail(errors, f"{CHECKSUMS_NAME}: no checksum entry for {name}")
    for name in sorted(set(seen) - expected_archives):
        fail(errors, f"{CHECKSUMS_NAME}: checksum entry for unexpected file {name}")
    for name, digest in sorted(seen.items()):
        artifact = dist / name
        if not artifact.is_file():
            continue  # reported by the dist-layout check
        actual = hashlib.sha256(artifact.read_bytes()).hexdigest()
        if actual != digest:
            fail(errors, f"checksum mismatch for {name}")


def safe_member_path(name: str, archive: str, errors: list[str]) -> bool:
    parts = name.replace("\\", "/").split("/")
    if name.startswith(("/", "\\")) or (len(parts[0]) == 2 and parts[0][1] == ":"):
        fail(errors, f"{archive}: unsafe absolute member path {name!r}")
        return False
    if ".." in parts:
        fail(errors, f"{archive}: unsafe member path with '..': {name!r}")
        return False
    return True


def check_tar_gz(path: Path, errors: list[str]) -> None:
    archive = path.name
    try:
        with tarfile.open(path, "r:gz") as tar:
            for member in tar.getmembers():
                safe_member_path(member.name, archive, errors)
                if member.isdir():
                    continue
                if not member.isfile():
                    fail(
                        errors,
                        f"{archive}: non-regular member {member.name!r} "
                        f"({member.type!r}); links/devices are not allowed",
                    )
                    continue
                if member.mode & 0o111:
                    basename = Path(member.name).name
                    if basename != BINARY_BASE or not member.mode & 0o555:
                        fail(errors, f"{archive}: unexpected executable member {member.name!r}")
    except (tarfile.TarError, OSError) as exc:
        fail(errors, f"{archive}: cannot read archive: {exc}")


def check_zip(path: Path, errors: list[str]) -> None:
    archive = path.name
    try:
        with zipfile.ZipFile(path) as zf:
            for info in zf.infolist():
                if info.is_dir():
                    continue
                if not safe_member_path(info.filename, archive, errors):
                    continue
                basename = Path(info.filename.replace("\\", "/")).name
                if basename.endswith(".exe") and basename != binary_name("windows"):
                    fail(errors, f"{archive}: unexpected executable member {info.filename!r}")
    except (zipfile.BadZipFile, OSError) as exc:
        fail(errors, f"{archive}: cannot read archive: {exc}")


def check_archive_contents(path: Path, os_name: str, errors: list[str]) -> None:
    archive = path.name
    wanted = binary_name(os_name)
    try:
        if path.suffix == ".zip":
            with zipfile.ZipFile(path) as zf:
                names = [info.filename for info in zf.infolist() if not info.is_dir()]
        else:
            with tarfile.open(path, "r:gz") as tar:
                names = [member.name for member in tar.getmembers() if member.isfile()]
    except (tarfile.TarError, zipfile.BadZipFile, OSError) as exc:
        fail(errors, f"{archive}: cannot read archive: {exc}")
        return
    executables = [n for n in names if Path(n.replace("\\", "/")).name == wanted]
    if len(executables) != 1:
        fail(errors, f"{archive}: expected exactly one {wanted}, found {len(executables)}")
    for doc in ("README.md", "CHANGELOG.md"):
        if not any(Path(n.replace("\\", "/")).name == doc for n in names):
            fail(errors, f"{archive}: {doc} not included in archive")


def extract_host_binary(archive: Path, os_name: str, dest: Path) -> Path | None:
    wanted = binary_name(os_name)
    try:
        if archive.suffix == ".zip":
            with zipfile.ZipFile(archive) as zf:
                match = [
                    info for info in zf.infolist()
                    if Path(info.filename.replace("\\", "/")).name == wanted
                ]
                if not match:
                    return None
                zf.extract(match[0], dest)
        else:
            with tarfile.open(archive, "r:gz") as tar:
                match = [
                    m for m in tar.getmembers()
                    if m.isfile() and Path(m.name.replace("\\", "/")).name == wanted
                ]
                if not match:
                    return None
                tar.extract(match[0], dest)
    except (tarfile.TarError, zipfile.BadZipFile, OSError):
        return None
    binary = dest / wanted
    if os_name != "windows":
        binary.chmod(0o755)
    return binary


def host_platform() -> tuple[str, str]:
    system = platform.system().lower()
    machine = platform.machine().lower()
    if machine in ("x86_64", "amd64"):
        arch = "amd64"
    elif machine in ("aarch64", "arm64"):
        arch = "arm64"
    else:
        arch = machine
    return system, arch


def check_host_binary(dist: Path, expected_archives: set[str], expected_version: str | None, errors: list[str]) -> None:
    system, arch = host_platform()
    if system not in ARCH_OS or arch not in ARCH_ARCH:
        fail(
            errors,
            f"host platform {system}/{arch} has no packaged archive; "
            "cannot execute the host binary",
        )
        return
    archive = dist / archive_name(system, arch)
    if archive.name not in expected_archives or not archive.is_file():
        fail(errors, f"host archive {archive.name} is missing; cannot execute host binary")
        return
    with tempfile.TemporaryDirectory(prefix="verify-release-") as tmp:
        binary = extract_host_binary(archive, system, Path(tmp))
        if binary is None:
            fail(errors, f"could not extract {binary_name(system)} from {archive.name}")
            return
        try:
            proc = subprocess.run(
                [str(binary), "info"],
                capture_output=True,
                text=True,
                timeout=HOST_TIMEOUT_SECONDS,
            )
        except (subprocess.SubprocessError, OSError) as exc:
            fail(errors, f"{binary_name(system)} info failed: {exc}")
            return
        if proc.returncode != 0:
            fail(errors, f"{binary_name(system)} info exited {proc.returncode}: {proc.stderr.strip()}")
            return
        try:
            info = json.loads(proc.stdout)
        except json.JSONDecodeError as exc:
            fail(errors, f"{binary_name(system)} info printed invalid JSON: {exc}")
            return
        if info.get("name") != "kimi-code":
            fail(errors, f"info JSON has unexpected name: {info.get('name')!r}")
        if info.get("protocol_version") != 1:
            fail(errors, f"info JSON has unexpected protocol_version: {info.get('protocol_version')!r}")
        hook_names = info.get("hook_names")
        if not isinstance(hook_names, list) or not hook_names or not all(isinstance(h, str) for h in hook_names):
            fail(errors, "info JSON has missing or invalid hook_names")
        try:
            proc = subprocess.run(
                [str(binary), "version"],
                capture_output=True,
                text=True,
                timeout=HOST_TIMEOUT_SECONDS,
            )
        except (subprocess.SubprocessError, OSError) as exc:
            fail(errors, f"{binary_name(system)} version failed: {exc}")
            return
        if proc.returncode != 0:
            fail(errors, f"{binary_name(system)} version exited {proc.returncode}: {proc.stderr.strip()}")
            return
        reported = proc.stdout.strip()
        if not reported.startswith(f"{BINARY_BASE} version "):
            fail(errors, f"{binary_name(system)} version printed unexpected output: {reported!r}")
        version_string = reported.removeprefix(f"{BINARY_BASE} version ")
        if expected_version is not None and version_string != expected_version:
            fail(
                errors,
                f"packaged binary reports version {version_string!r}, "
                f"expected {expected_version!r}",
            )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", default="dist", help="GoReleaser output directory (default: dist)")
    parser.add_argument(
        "--expected-version",
        default=None,
        help="version string that must be embedded in the packaged binary",
    )
    args = parser.parse_args()

    expected_archives = {
        archive_name(os_name, arch)
        for os_name in ARCH_OS
        for arch in ARCH_ARCH
    }

    errors: list[str] = []
    dist = Path(args.dist)
    check_dist_layout(dist, expected_archives, errors)
    if dist.is_dir():
        check_checksums(dist, expected_archives, errors)
        for path in sorted(dist.iterdir()):
            if path.name not in expected_archives or not path.is_file():
                continue
            if path.name.endswith(".tar.gz"):
                check_tar_gz(path, errors)
                check_archive_contents(path, "linux", errors)
            elif path.suffix == ".zip":
                check_zip(path, errors)
                check_archive_contents(path, "windows", errors)
        check_host_binary(dist, expected_archives, args.expected_version, errors)

    if errors:
        for message in errors:
            print(f"ERROR: {message}", file=sys.stderr)
        print(f"verification failed with {len(errors)} error(s)", file=sys.stderr)
        return 1
    print("verification passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
