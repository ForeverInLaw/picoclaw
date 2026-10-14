#!/usr/bin/env python3
"""Real launcher/gateway/agent acceptance with temporary state and fake providers."""
import argparse
import hashlib
import http.server
import json
import os
import re
import shutil
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def command(*args, timeout=120, check=True):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise AssertionError(f"{args[0]} failed ({result.returncode}):\n{result.stdout}\n{result.stderr}")
    return result


def request(url, method="GET", data=None, cookie=None):
    headers = {"Content-Type": "application/json"}
    if cookie:
        headers["Cookie"] = cookie
    req = urllib.request.Request(
        url, data=None if data is None else json.dumps(data).encode(), method=method, headers=headers
    )
    try:
        response = urllib.request.urlopen(req, timeout=3)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read().decode(), response.headers


def published_url(name, port):
    address = command("docker", "port", name, f"{port}/tcp").stdout.strip().splitlines()[0]
    return "http://" + address


def wait_http(url, name):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        try:
            if request(url)[0] == 200:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise AssertionError(command("docker", "logs", name).stdout)


class FakeProvider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path.endswith("/models"):
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"data":[{"id":"fixture"}]}')
            return
        self.server.network_calls += 1
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"network-available")

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if not body.get("tools") and body["messages"][-1]["role"] != "tool":
            self.server.summaries += 1
            message = {"role": "assistant", "content": "fixture-summary"}
            finish = "stop"
        elif body["messages"][-1]["role"] == "tool":
            self.server.requests += 1
            self.server.tool_output = body["messages"][-1]["content"]
            message = {"role": "assistant", "content": "packaging-complete"}
            finish = "stop"
        else:
            self.server.requests += 1
            if self.server.scenario == "shell":
                name = "exec"
                url = f"http://127.0.0.1:{self.server.server_port}/network"
                script = (
                    "printf saved > workspace-write; "
                    "if [ -e /fixtures/secret ]; then printf OUTSIDE_VISIBLE; fi; "
                    'if [ -n "$UNDECLARED_MCP_SECRET" ]; then printf ENV_VISIBLE; fi; '
                    "if printf mutation > /usr/share/packaging-fixture/shell-write; then printf RUNTIME_WRITABLE; fi; "
                    "if cat /proc/1/environ | grep -q UNDECLARED_MCP_SECRET; then printf PROC_ENV_VISIBLE; fi; "
                    f"curl -fsS --max-time 1 {url} || printf network-blocked; printf '|shell-ok'"
                )
                arguments = {"command": script}
            else:
                name = next(
                    tool["function"]["name"] for tool in body["tools"]
                    if tool["function"]["name"].endswith("_probe")
                )
                arguments = {}
            message = {
                "role": "assistant", "content": "",
                "tool_calls": [{
                    "id": "fixture-action", "type": "function",
                    "function": {"name": name, "arguments": json.dumps(arguments)},
                }],
            }
            finish = "tool_calls"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"choices": [{"message": message, "finish_reason": finish}]}).encode())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--launcher", default="picoclaw:launcher-local")
    parser.add_argument("--gateway", default="picoclaw:local")
    parser.add_argument("--full", default="picoclaw:full-local")
    args = parser.parse_args()
    containers = []
    networks = []
    provider = http.server.HTTPServer(("0.0.0.0", 0), FakeProvider)
    provider.scenario, provider.tool_output = "shell", ""
    provider.requests = provider.summaries = provider.network_calls = 0
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="picoclaw-container-") as temp:
            base = Path(temp)
            os.chmod(base, 0o755)
            state, outside = base / "state", base / "outside"
            state.mkdir(mode=0o777)
            os.chmod(state, 0o777)  # Temporary bind mount must be writable by image UID 1000.
            outside.mkdir(mode=0o777)
            runtime = base / "runtime"
            runtime.mkdir()
            os.chmod(runtime, 0o777)
            (outside / "secret").write_text("outside-fixture")
            command("docker", "run", "--rm", "--entrypoint", "picoclaw", "-v", f"{state}:/data", args.launcher, "onboard")
            for template in ["AGENT.md", "SOUL.md", "TOOLS.md", "USER.md", "skills/github/SKILL.md"]:
                assert (state / "workspace" / template).stat().st_size, template
            fixture = base / "mcp-fixture"
            command("env", "CGO_ENABLED=0", "go", "test", "-c", "-o", str(fixture), "./pkg/mcp")
            shutil.copy2(fixture, state / "workspace" / "mcp-fixture")
            prepared = base / "prepared.json"
            cfg = {
                "version": 3,
                "agents": {"defaults": {
                    "model_name": "fixture", "workspace": "/data/workspace",
                    "context_window": 131072, "max_tokens": 2048, "restrict_to_workspace": False,
                }},
                "model_list": [{
                    "model_name": "fixture", "model": "openai/fixture", "api_key": "fixture-key",
                    "api_base": f"http://fixture-provider:{provider.server_port}",
                }],
                "heartbeat": {"enabled": False},
                "gateway": {"host": "0.0.0.0", "port": 18790},
                "channel_list": {"pico": {"type": "pico", "enabled": True, "settings": {"token": "fixture-pico-token"}}},
                "tools": {
                    "filter_sensitive_data": False,
                    "exec": {"enabled": True, "enable_deny_patterns": False, "allow_remote": True},
                },
            }
            prepared.write_text(json.dumps(cfg))
            mounts = [
                "-v", f"{state}:/data", "-v", f"{prepared}:/data/config.json:ro",
                "-v", f"{outside}:/fixtures", "-v", f"{runtime}:/usr/share/packaging-fixture",
            ]
            before = hashlib.sha256(prepared.read_bytes()).hexdigest()
            name = f"picoclaw-acceptance-{os.getpid()}"
            command(
                "docker", "run", "-d", "--name", name,
                "--add-host", "fixture-provider:host-gateway", *mounts,
                "-p", "127.0.0.1::18800", "-p", "127.0.0.1::18790", args.launcher,
            )
            containers.append(name)
            url = published_url(name, 18800)
            wait_http(url + "/api/auth/status", name)
            wait_http(published_url(name, 18790) + "/health", name)
            assert json.loads(request(url + "/api/auth/status")[1])["initialized"] is False
            assert request(url + "/api/config")[0] == 401
            html = request(url + "/launcher-setup")[1]
            asset = re.search(r'src="(/assets/[^\"]+\.js)"', html).group(1)
            assert request(url + asset)[0] == 200
            password = "container-test-password"
            assert request(url + "/api/auth/setup", "POST", {"password": password, "confirm": password})[0] == 200
            status, _, headers = request(url + "/api/auth/login", "POST", {"password": password})
            assert status == 200
            cookie = headers["Set-Cookie"].split(";", 1)[0]
            assert request(url + "/api/config", cookie=cookie)[0] == 200
            assert request(url + "/api/config", "PATCH", {"agents": {"defaults": {"temperature": 0.5}}}, cookie)[0] >= 400
            assert hashlib.sha256(prepared.read_bytes()).hexdigest() == before
            assert (state / "launcher-auth.db").is_file()
            command("docker", "restart", "--time", "10", name)
            url = published_url(name, 18800)
            wait_http(url + "/api/auth/status", name)
            wait_http(published_url(name, 18790) + "/health", name)
            assert json.loads(request(url + "/api/auth/status")[1])["initialized"] is True
            assert request(url + "/api/config", cookie=cookie)[0] == 401
            assert request(url + "/api/auth/login", "POST", {"password": password})[0] == 200
            command("docker", "stop", "--time", "10", name)
            assert command("docker", "inspect", "--format", "{{.State.ExitCode}}", name).stdout.strip() == "0"
            assert provider.requests == 0
            print("launcher: embedded UI, setup/login, gateway startup/restart, read-only config, writable auth/workspace, shutdown PASS")

            overlay = base / "image.yml"
            overlay.write_text(json.dumps({"services": {"picoclaw-agent": {"image": args.gateway}}}))
            networks.append(name + "_default")
            command(
                "docker", "compose", "-p", name, "-f", "docker/docker-compose.yml",
                "-f", "docker/docker-compose.isolation.yml", "-f", str(overlay),
                "run", "--rm", "--no-deps", "--entrypoint", "bwrap", "-v", f"{state}:/data",
                "picoclaw-agent", "--unshare-user", "--unshare-pid", "--die-with-parent",
                "--ro-bind", "/", "/", "--proc", "/proc", "/bin/true",
            )
            print("compose: opt-in isolation profile path and namespace/proc prerequisites PASS")

            cfg["model_list"][0]["api_base"] = f"http://127.0.0.1:{provider.server_port}"
            security = [
                "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                "--security-opt", f"seccomp={ROOT / 'docker/seccomp-bubblewrap.json'}",
                "--security-opt", "apparmor=unconfined", "--security-opt", "systempaths=unconfined",
            ]
            for scenario, enabled, offline, grants in [
                ("shell", True, False, []),
                ("shell", True, False, security),
                ("shell", True, True, security),
                ("mcp", True, False, []),
                ("mcp", True, False, security),
                ("mcp", True, True, security),
                ("mcp", False, False, []),
                ("shell", False, False, []),
            ]:
                provider.scenario, provider.tool_output = scenario, ""
                provider.requests = provider.summaries = provider.network_calls = 0
                cfg["isolation"] = {"enabled": enabled, "disable_network": offline}
                cfg["tools"]["mcp"] = {"enabled": scenario == "mcp", "servers": {"fixture": {
                    "enabled": True, "deferred": False, "command": "/data/workspace/mcp-fixture",
                    "args": ["-test.run=^TestIsolationMCPServerProcess$", "/fixtures/secret"],
                    "env": {
                        "IVO_MCP_CHILD": "1", "MCP_WORKSPACE": "/data/workspace",
                        "MCP_OUTSIDE_FILE": "/fixtures/secret", "MCP_HOST_PID": "99999999",
                        "MCP_DECLARED_TOKEN": "declared-value",
                        "MCP_RUNTIME_WRITE": "/usr/share/packaging-fixture/mcp-write",
                        "MCP_NETWORK_ENDPOINT": f"http://127.0.0.1:{provider.server_port}/network",
                    },
                }}}
                prepared.write_text(json.dumps(cfg))
                session = f"container:{scenario}:{enabled}:{offline}:{bool(grants)}"
                result = command(
                    "docker", "run", "--rm", "--network", "host", *grants, *mounts,
                    "-e", "UNDECLARED_MCP_SECRET=inherited-secret", "--entrypoint", "picoclaw",
                    args.full if scenario == "mcp" else args.gateway,
                    "agent", "-s", session, "-m", "packaging-contract", check=False,
                )
                if enabled and not grants:
                    assert "bubblewrap" in result.stdout + result.stderr + provider.tool_output, result.stdout + result.stderr
                    if scenario == "mcp":
                        assert result.returncode != 0 and provider.requests == 0
                    else:
                        assert "shell-ok" not in provider.tool_output
                    print(f"{scenario}: default container policy denies enabled sandbox explicitly PASS")
                    continue
                assert result.returncode == 0, result.stdout + result.stderr
                assert provider.requests == 2, provider.requests
                if scenario == "shell":
                    assert "shell-ok" in provider.tool_output, provider.tool_output
                    for forbidden in ["OUTSIDE_VISIBLE", "ENV_VISIBLE", "RUNTIME_WRITABLE", "PROC_ENV_VISIBLE"]:
                        assert (forbidden in provider.tool_output) is (not enabled), provider.tool_output
                    assert (state / "workspace" / "workspace-write").read_text() == "saved"
                else:
                    data = json.loads(provider.tool_output)
                    assert data["declared"] == "declared-value" and data["arguments"] == "{}", data
                    assert data["workspace_written"] is True
                    assert data["inherited"] == ("" if enabled else "inherited-secret"), data
                    assert data["outside"] == ("" if enabled else "outside-fixture"), data
                    assert data["proc_secret_visible"] is (not enabled), data
                    assert data["runtime_written"] is (not enabled), data
                assert provider.network_calls == (0 if offline else 1), provider.network_calls
                print(f"{scenario}: isolated={enabled} offline={offline} actual product tool, credentials/mount/env/proc/network PASS")
            for image in [args.launcher, args.gateway, args.full]:
                metadata = json.loads(command("docker", "image", "inspect", image).stdout)[0]
                print(json.dumps({"image": image, "id": metadata["Id"], "labels": metadata["Config"].get("Labels", {})}))
            print("acceptance checkout:", command("git", "rev-parse", "HEAD").stdout.strip())
    finally:
        provider.shutdown()
        provider.server_close()
        for name in containers:
            command("docker", "rm", "--force", name, check=False)
        for name in networks:
            command("docker", "network", "rm", name, check=False)


if __name__ == "__main__":
    main()
