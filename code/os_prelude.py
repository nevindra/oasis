import sys
import json
import os
import urllib.request

# --- Protocol setup ---
# Reserve real stdout (fd 1) for JSON protocol messages.
# Redirect print() to stderr so debug output does not corrupt the protocol.
_proto_out = open(1, 'w', buffering=1)
sys.stdout = sys.stderr

_final_result = None
_output_files = []

_CALLBACK_URL = os.environ.get('_SANDBOX_CALLBACK_URL', '')
_EXECUTION_ID = os.environ.get('_SANDBOX_EXECUTION_ID', '')


def call_tool(name, args=None):
    """Call an agent tool via the Oasis callback URL and return the result.

    Blocks until the tool returns. Raises RuntimeError on tool failure.

    Example:
        data = call_tool('web_search', {'query': 'latest pandas release'})
        content = call_tool('file_read', {'path': 'config.yaml'})
    """
    if not _CALLBACK_URL:
        raise RuntimeError("call_tool: no callback URL configured")

    payload = json.dumps({
        "execution_id": _EXECUTION_ID,
        "name": name,
        "args": args or {},
    }).encode('utf-8')
    req = urllib.request.Request(
        _CALLBACK_URL,
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            body = resp.read()
    except urllib.error.HTTPError as e:
        raise RuntimeError(
            f"Tool '{name}': HTTP {e.code}: "
            f"{e.read().decode('utf-8', errors='replace')}"
        )
    except Exception as e:
        raise RuntimeError(f"Tool '{name}': request failed: {e}")

    result = json.loads(body)
    if result.get("error"):
        raise RuntimeError(f"Tool '{name}' failed: {result['error']}")

    data = result.get("data", "")
    try:
        return json.loads(data) if isinstance(data, str) and data else data
    except (json.JSONDecodeError, TypeError):
        return data


def call_tools_parallel(calls):
    """Call multiple tools in parallel. Returns a list of results in order.

    Each element of calls is a (name, args) tuple.
    If any tool raises an exception, it is re-raised after all threads complete.

    Example:
        pages = call_tools_parallel([
            ('http_fetch', {'url': 'https://example.com/a'}),
            ('http_fetch', {'url': 'https://example.com/b'}),
        ])
    """
    import threading

    results = [None] * len(calls)
    errors = [None] * len(calls)

    def _worker(i, name, args):
        try:
            results[i] = call_tool(name, args)
        except Exception as exc:
            errors[i] = exc

    threads = [
        threading.Thread(target=_worker, args=(i, name, args or {}))
        for i, (name, args) in enumerate(calls)
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    for err in errors:
        if err is not None:
            raise err

    return results


def set_result(data, files=None):
    """Set the structured result to return to the caller.

    Call this once at the end of your code. The data is JSON-serialized and
    returned in the 'output' field of the HTTP response.

    Args:
        data:  any JSON-serializable value (dict, list, str, int, etc.)
        files: optional list of relative file paths to include in the response
               as base64-encoded files. Example: ['chart.png', 'report.csv']

    Example:
        set_result({"summary": "done", "count": 42}, files=["chart.png"])
    """
    global _final_result, _output_files
    _final_result = data
    if files is not None:
        _output_files = list(files)


def install_package(name):
    """Install a Python package at runtime using pip.

    The container boundary provides isolation. Installed packages persist
    for the lifetime of the container.

    Example:
        install_package('httpx')
        import httpx
    """
    import subprocess as _sp
    print(f"[sandbox] pip install {name}...", file=sys.stderr)
    result = _sp.run(
        [sys.executable, '-m', 'pip', 'install', '-q', name],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"pip install {name} failed: {result.stderr}")
    print(f"[sandbox] {name} installed", file=sys.stderr)


# --- User code starts below ---
