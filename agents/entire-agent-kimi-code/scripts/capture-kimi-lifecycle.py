"""Real lifecycle-hook capture for PreCompact/PostCompact, StopFailure, Interrupt, SessionEnd.

Requires installed Kimi CLI with existing authentication in place; never copies
credentials and never rewrites config beyond a byte-guarded temporary hook
append that is restored in ``finally``. Captures raw hook payloads into a
disposable output directory; run ``sanitize`` to turn one raw capture into a
checked-in fixture (session ids, cwd, and titles replaced with stable
placeholders). Never prints configuration content.

Driver:  py -3 scripts/capture-kimi-lifecycle.py --output <raw-capture-dir>
Hook:    py -3 scripts/capture-kimi-lifecycle.py capture <Event>
Sanitize hook JSON:  py -3 ... sanitize <raw.json> <fixture.json> --repo <temp-repo>
Sanitize wire log:   py -3 ... sanitize-transcript <raw.jsonl> <fixture.jsonl> --repo <temp-repo>

Triggers, all real and non-destructive:
- PreCompact/PostCompact: ACP builtin slash command ``/compact`` on a session
  with history.
- Interrupt: ACP ``session/cancel`` while a turn is running a Bash sleep.
- SessionEnd: session closed normally via ACP ``session/close``. Headless
  process exit alone does not emit SessionEnd in the tested version.
- StopFailure: isolated loopback-only HTTP 400 provider, without real credentials.

Interactive /exit can also be tested through a PTY. This driver uses ACP and reports missing events instead of fabricating captures.
"""
import argparse
import csv
import io
import queue
import threading
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

HOOK_EVENTS = ["PreCompact", "PostCompact", "StopFailure", "Interrupt", "SessionEnd"]
SANITIZED_SESSION = "session_lifecycle"
SANITIZED_REPO = "/test/repo"
SANITIZED_TITLE = "Lifecycle Probe Session"


def hook_mode():
    if not os.environ.get("KIMI_LIFECYCLE_OUT"):
        return
    event = json.load(sys.stdin)
    out = Path(os.environ["KIMI_LIFECYCLE_OUT"])
    out.mkdir(parents=True, exist_ok=True)
    name = out / (event.get("hook_event_name", sys.argv[2]) + "-" + str(time.time_ns()) + ".json")
    name.write_text(json.dumps(event), encoding="utf-8")
    sys.exit(0)


def sanitize_string(value, repo):
    if value == repo or value.startswith(repo + "/") or value.startswith(repo + "\\"):
        return SANITIZED_REPO + value[len(repo):].replace("\\", "/")
    value = re.sub(r"wd_[A-Za-z0-9_]+", "wd_<workspace>", value)
    value = re.sub(r"session_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
                   SANITIZED_SESSION, value)
    value = re.sub(r"entire-kimi-lifecycle-[A-Za-z0-9_]+", "entire-kimi-lifecycle-repo", value)
    value = re.sub(r"(Users[/\\])[A-Za-z0-9_.\-]+", r"\1<user>", value)
    return value


def sanitize_obj(obj, repo):
    if isinstance(obj, dict):
        out = {}
        for key, value in obj.items():
            if key in ("session_id", "sessionId") and isinstance(value, str):
                out[key] = SANITIZED_SESSION
            elif key in ("session_title", "sessionTitle", "title") and isinstance(value, str) and value:
                out[key] = SANITIZED_TITLE
            else:
                out[key] = sanitize_obj(value, repo)
        return out
    if isinstance(obj, list):
        return [sanitize_obj(item, repo) for item in obj]
    if isinstance(obj, str):
        return sanitize_string(obj, repo)
    return obj


def sanitize_command(raw, fixture, repo):
    data = json.loads(Path(raw).read_text(encoding="utf-8"))
    Path(fixture).write_text(json.dumps(sanitize_obj(data, repo), indent=2) + "\n", encoding="utf-8")


def sanitize_transcript_command(raw, fixture, repo):
    """Sanitize a raw wire log: drop payload-heavy records (``llm.*`` requests
    and ``profile.bind`` embed the full model request: system prompt, tool
    schemas, local skill paths) and replace session ids, workspace names, and
    user paths with placeholders."""
    lines = []
    for line in Path(raw).read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        record = json.loads(line)
        kind = str(record.get("type", ""))
        allowed = (kind in ("metadata", "turn.prompt", "context.append_loop_event",
                            "context.append_message", "context.apply_compaction", "context.undo", "context.clear",
                            "usage.record", "turn.cancel", "turn.ended", "prompt.aborted",
                            "prompt.completed", "agent.turn.started", "agent.turn.ended")
                   or kind.startswith(("subagent.", "full_compaction.", "turn.step.")))
        if not allowed:
            continue
        lines.append(json.dumps(sanitize_obj(record, repo)))
    Path(fixture).write_text("\n".join(lines) + "\n", encoding="utf-8")


class AcpError(RuntimeError):
    pass


class AcpClient:
    """Minimal Agent Client Protocol client over stdio."""

    def __init__(self, proc):
        self.proc = proc
        self.next_id = 1
        self.closed = False
        self.messages = queue.Queue()
        def reader():
            for line in self.proc.stdout:
                try:
                    self.messages.put(json.loads(line))
                except (ValueError, UnicodeError):
                    continue
            self.messages.put(None)
        threading.Thread(target=reader, daemon=True).start()

    def _send(self, payload):
        self.proc.stdin.write((json.dumps(payload) + "\n").encode("utf-8"))
        self.proc.stdin.flush()

    def _read_line(self, timeout):
        """Read one JSON-RPC line, or None when the timeout lapses first.

        Messages other than the awaited response are discarded; only one
        request is ever in flight, so nothing needs buffering.
        """
        try:
            message = self.messages.get(timeout=timeout)
        except queue.Empty:
            return None
        if message is None:
            self.closed = True
            raise AcpError("ACP server closed stdout")
        return message

    def request(self, method, params, timeout=300):
        rid = self.next_id
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        return self.await_response(rid, timeout)

    def await_response(self, rid, timeout=300):
        deadline = time.time() + timeout
        while True:
            remaining = deadline - time.time()
            if remaining <= 0:
                raise AcpError(f"ACP request {rid} timed out")
            message = self._read_line(remaining)
            if message is None:
                continue
            if message.get("id") == rid and message.get("method") is None:
                if "error" in message:
                    raise AcpError(f"ACP request {rid} failed: {message['error']}")
                return message.get("result")

    def notify(self, method, params):
        self._send({"jsonrpc": "2.0", "method": method, "params": params})

    def prompt_and_cancel(self, session_id, text, timeout=120):
        """Send a prompt, cancel it at the first streamed delta, settle the reply."""
        rid = self.next_id
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": rid, "method": "session/prompt",
                    "params": {"sessionId": session_id,
                               "prompt": [{"type": "text", "text": text}]}})
        deadline = time.time() + timeout
        running = False
        while time.time() < deadline:
            remaining = deadline - time.time()
            message = self._read_line(remaining) if remaining > 0 else None
            if message is None:
                break
            if message.get("id") == rid and message.get("method") is None:
                raise AcpError("prompt settled before any output to cancel")
            if message.get("method") == "session/update":
                update = message["params"].get("update", {})
                if update.get("sessionUpdate") in ("assistant_message_chunk", "agent_message_chunk",
                                                   "agent_thought_chunk", "thinking_message_chunk",
                                                   "tool_call"):
                    running = True
                    break
        if not running:
            raise AcpError("agent never streamed output to cancel")
        self.notify("session/cancel", {"sessionId": session_id})
        return self.await_response(rid, timeout)

    def prompt_collecting_chunks(self, session_id, text, timeout=300):
        """Send a prompt and collect its agent_message_chunk texts for diagnosis."""
        rid = self.next_id
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": rid, "method": "session/prompt",
                    "params": {"sessionId": session_id,
                               "prompt": [{"type": "text", "text": text}]}})
        chunks = []
        deadline = time.time() + timeout
        while time.time() < deadline:
            remaining = deadline - time.time()
            message = self._read_line(remaining) if remaining > 0 else None
            if message is None:
                continue
            if message.get("id") == rid and message.get("method") is None:
                if "error" in message:
                    raise AcpError(f"ACP request {rid} failed: {message['error']}")
                return message.get("result"), chunks
            if message.get("method") == "session/update":
                update = message["params"].get("update", {})
                if (update.get("sessionUpdate") == "agent_message_chunk"
                        and isinstance(update.get("content", {}).get("text"), str)):
                    chunks.append(update["content"]["text"])
        raise AcpError(f"ACP request {rid} timed out")


def wait_for_captures(out, events, timeout):
    """Poll the capture directory until every event lands or the timeout lapses."""
    deadline = time.time() + timeout
    captured = set()
    while time.time() < deadline:
        captured = {f.name.rsplit("-", 1)[0] for f in out.glob("*.json")}
        if all(event in captured for event in events):
            return {"captured": sorted(captured), "waited": round(timeout - (deadline - time.time()), 1)}
        time.sleep(3)
    return {"captured": sorted(captured), "waited": timeout,
            "never_arrived": [e for e in events if e not in captured]}


def capture_acp(repo, out, env):
    env = dict(env)
    env.pop("CODEX_THREAD_ID", None)
    env.pop("CODEX_SESSION_ID", None)
    proc = subprocess.Popen(
        ["kimi", "acp"], cwd=repo, env=env,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    results = {}
    client = AcpClient(proc)
    try:
        client.request("initialize", {
            "protocolVersion": 1,
            "capabilities": {},
            "clientInfo": {"name": "entire-kimi-lifecycle-probe", "version": "0.0.0"},
        })
        client.notify("notifications/initialized", {})
        session = client.request("session/new", {"cwd": str(repo), "mcpServers": []})
        session_id = session["sessionId"]
        results["session_id"] = session_id
        client.request("session/set_mode", {"sessionId": session_id, "modeId": "auto"})

        ready = client.request("session/prompt", {
            "sessionId": session_id,
            "prompt": [{"type": "text", "text":
                        "Reply with exactly LIFECYCLE_ACP_READY and nothing else. Do not use tools."}],
        })
        results["ready_stop_reason"] = ready.get("stopReason")

        compact, compact_chunks = client.prompt_collecting_chunks(session_id, "/compact")
        results["compact_stop_reason"] = compact.get("stopReason")
        results["compact_chunks"] = compact_chunks

        # Compaction runs in the background; a second prompt while it is still
        # active is rejected as busy, and its hooks land only once it runs.
        # Wait for the PostCompact hook to land (or the timeout to lapse)
        # before the next prompt, and record what the wait observed.
        results["compact_hook_wait"] = wait_for_captures(
            out, ["PreCompact", "PostCompact"], timeout=180)

        try:
            results["cancel_stop_reason"] = client.prompt_and_cancel(
                session_id,
                "Reply with a detailed 300-word explanation of how water cooling loops work, "
                "written as one continuous paragraph. Do not stop early.")
        except AcpError as error:
            results["cancel_error"] = str(error)

        time.sleep(10)  # let fire-and-forget hooks finish writing

        try:
            results["closed_stop_reason"] = client.request(
                "session/close", {"sessionId": session_id})
            results["closed"] = True
        except AcpError as error:
            results["close_error"] = str(error)
    finally:
        try:
            proc.stdin.close()
        except OSError:
            pass
        try:
            proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=30)
    return results


def private_atomic_write(path, data, expected=None):
    """Secure an empty sibling file before putting sensitive config bytes in it."""
    fd, name = tempfile.mkstemp(prefix=".entire-lifecycle-", dir=path.parent)
    try:
        if os.name == "nt":
            if expected is not None:
                # The config keeps its original descriptor; backups stay private.
                subprocess.run(["powershell", "-NoProfile", "-NonInteractive", "-Command",
                    "$ErrorActionPreference='Stop'; $a=[IO.File]::GetAccessControl($env:ENTIRE_ACL_SOURCE); "
                    "[IO.File]::SetAccessControl($env:ENTIRE_ACL_DESTINATION,$a)"],
                    env=dict(os.environ,ENTIRE_ACL_SOURCE=str(path),ENTIRE_ACL_DESTINATION=name),
                    check=True,capture_output=True)
            else:
                output = subprocess.check_output(["whoami", "/user", "/fo", "csv", "/nh"], text=True)
                sid = next(csv.reader(io.StringIO(output.strip())))[1]
                subprocess.run(["icacls", name, "/inheritance:r", "/grant:r", f"*{sid}:(F)"],
                               check=True, capture_output=True)
        else:
            os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as target:
            fd = -1
            target.write(data)
            target.flush()
            os.fsync(target.fileno())
        if expected is not None and path.read_bytes() != expected:
            raise RuntimeError("Config changed concurrently before replacement; preserving it")
        os.replace(name, path)
    finally:
        if fd >= 0:
            os.close(fd)
        Path(name).unlink(missing_ok=True)


def driver(args):
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=True)
    repo = Path(tempfile.mkdtemp(prefix="entire-kimi-lifecycle-"))
    env = dict(os.environ)
    env.pop("CODEX_THREAD_ID", None)
    env.pop("CODEX_SESSION_ID", None)
    env.pop("CLAUDE_SESSION_ID", None)
    env["KIMI_LIFECYCLE_OUT"] = str(out)
    env["KIMI_CODE_TRUST_WORKSPACE"] = "1"

    def run(*cmd, timeout=240):
        p = subprocess.run(cmd, cwd=repo, env=env, capture_output=True, timeout=timeout)
        if p.returncode:
            raise RuntimeError(f"{cmd[0]} {cmd[1:]} exited {p.returncode}: {p.stderr.decode(errors='replace')}")
        return p.stdout

    run("git", "init")
    run("git", "config", "user.name", "Kimi Lifecycle Probe")
    run("git", "config", "user.email", "integration@example.invalid")
    run("git", "commit", "--allow-empty", "-m", "initial")

    home = Path(os.environ.get("KIMI_CODE_HOME", str(Path.home() / ".kimi-code")))
    config = home / "config.toml"
    original = config.read_bytes()
    script = Path(__file__).resolve()
    addition = "\n# BEGIN temporary Entire lifecycle capture\n"
    for event in HOOK_EVENTS:
        command = f'py -3 "{script}" capture {event}'
        addition += f"\n[[hooks]]\nevent = {json.dumps(event)}\ncommand = {json.dumps(command)}\ntimeout = 30\n"
    installed = original + addition.encode()
    lock_path = config.with_name(config.name + ".entire-lock")
    lock_fd = os.open(lock_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    try:
        if config.read_bytes() != original:
            raise RuntimeError("Config changed before capture setup; retry")
        backup = config.with_name(config.name + f".entire-lifecycle.{time.time_ns()}.bak")
        private_atomic_write(backup, original)
        private_atomic_write(config, installed, expected=original)
        acp_results = capture_acp(repo, out, env)
        print("ACP scenario:", json.dumps(acp_results, indent=2))

        # Deterministic loopback-only provider failure: no private model alias,
        # LAN dependency, or real credentials are needed for this path.
        failure = subprocess.run(
            [sys.executable, str(script.with_name("smoke-failure.py"))],
            cwd=repo, env=env, capture_output=True, timeout=120, check=True)
        failure_result = json.loads(failure.stdout)
        failure_capture = Path(failure_result["evidence"]) / "StopFailure.json"
        (out / f"StopFailure-{time.time_ns()}.json").write_bytes(failure_capture.read_bytes())

        time.sleep(5)  # let trailing SessionEnd hooks finish writing

        # Preserve raw wire logs of this run's sessions for transcript
        # fixtures (sanitize with the sanitize-transcript subcommand).
        wires = sorted((home / "sessions").glob(
            f"wd_{repo.name}_*/session_*/agents/main/wire.jsonl"), key=lambda p: p.stat().st_mtime)
        for wire in wires:
            session_dir = wire.parent.parent.parent
            (out / f"wire-{session_dir.name}.jsonl").write_bytes(wire.read_bytes())
        print("preserved wire logs:", [p.name for p in out.glob("wire-*.jsonl")])

        captured = {capture.name.rsplit("-", 1)[0] for capture in out.glob("*.json")}
        missing = [event for event in HOOK_EVENTS if event not in captured]
        print(json.dumps({
            "captured": sorted(captured),
            "missing": missing,
            "failure_run_exit_code": failure_result["kimi_exit"],
            "failure_run_stderr": failure.stderr.decode(errors="replace")[:2000],
            "output_dir": str(out),
            "repo": str(repo),
        }, indent=2))
        if missing:
            raise RuntimeError("Missing native lifecycle captures: " + ", ".join(missing))
    finally:
        try:
            current = config.read_bytes()
            block = addition.encode()
            if current.count(block) == 1:
                private_atomic_write(config, current.replace(block, b"", 1), expected=current)
                print("Temporary capture hooks removed; unrelated config preserved")
            elif current != original:
                raise RuntimeError("Capture block changed concurrently; preserving config for reconciliation")
        finally:
            os.close(lock_fd)
            lock_path.unlink()



def main():
    if len(sys.argv) > 1 and sys.argv[1] == "capture":
        hook_mode()
        return
    if len(sys.argv) > 1 and sys.argv[1] == "sanitize":
        parser = argparse.ArgumentParser()
        parser.add_argument("raw")
        parser.add_argument("fixture")
        parser.add_argument("--repo", required=True)
        parser.add_argument("--transcript", action="store_true")
        args = parser.parse_args(sys.argv[2:])
        if args.transcript:
            sanitize_transcript_command(args.raw, args.fixture, args.repo)
        else:
            sanitize_command(args.raw, args.fixture, args.repo)
        return
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    driver(parser.parse_args())


if __name__ == "__main__":
    main()
