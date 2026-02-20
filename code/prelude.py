import sys, json, os

# --- Protocol setup ---
# Reserve real stdout (fd 1) for JSON protocol messages.
# Redirect print() to stderr so user output doesn't corrupt the protocol.
_proto_out = open(1, 'w', buffering=1)
_proto_in = open(0, 'r')
sys.stdout = sys.stderr

_call_id = 0
_final_result = None

def call_tool(name, args=None):
    """Call an agent tool and return the result.

    Example:
        content = call_tool('file_read', {'path': '.env'})
        result = call_tool('http_fetch', {'url': 'https://api.example.com/data'})
    """
    global _call_id
    _call_id += 1
    cid = f"tc_{_call_id}"
    msg = json.dumps({"type": "tool_call", "id": cid, "name": name, "args": args or {}})
    _proto_out.write(msg + '\n')
    _proto_out.flush()
    line = _proto_in.readline()
    if not line:
        raise RuntimeError(f"Tool '{name}': no response from agent (pipe closed)")
    resp = json.loads(line)
    if resp.get("type") == "tool_error":
        raise RuntimeError(f"Tool '{name}' failed: {resp['error']}")
    try:
        return json.loads(resp["data"])
    except (json.JSONDecodeError, TypeError, KeyError):
        return resp.get("data", "")

def call_tools_parallel(calls):
    """Call multiple tools in parallel. Returns list of results in order.

    Example:
        results = call_tools_parallel([
            ('file_read', {'path': 'a.py'}),
            ('file_read', {'path': 'b.py'}),
        ])
    """
    global _call_id
    batch = []
    for name, args in calls:
        _call_id += 1
        batch.append({"id": f"tc_{_call_id}", "name": name, "args": args or {}})
    msg = json.dumps({"type": "tool_calls_parallel", "calls": batch})
    _proto_out.write(msg + '\n')
    _proto_out.flush()
    line = _proto_in.readline()
    if not line:
        raise RuntimeError("Parallel tool calls: no response from agent (pipe closed)")
    resp = json.loads(line)
    results = []
    for r in resp.get("results", []):
        if r.get("error"):
            results.append(RuntimeError(f"Tool failed: {r['error']}"))
        else:
            try:
                results.append(json.loads(r["data"]))
            except (json.JSONDecodeError, TypeError, KeyError):
                results.append(r.get("data", ""))
    return results

def set_result(data):
    """Set the final result to return to the agent.

    Example:
        set_result({"summary": "Found 5 issues", "items": [...]})
    """
    global _final_result
    _final_result = data

# --- Safety guards ---

# Workspace isolation: restrict filesystem operations to workspace directory.
_workspace = os.path.realpath(os.environ.get("_OASIS_WORKSPACE", os.getcwd()))

_original_open = open.__class__.__call__  # save builtins.open
import builtins as _builtins
_builtin_open = _builtins.open

def _safe_open(path, *args, **kwargs):
    # Allow special paths (stdin/stdout/stderr fds, /dev/null)
    if isinstance(path, int):
        return _builtin_open(path, *args, **kwargs)
    resolved = os.path.realpath(os.path.join(_workspace, str(path)))
    if not resolved.startswith(_workspace + os.sep) and resolved != _workspace:
        raise PermissionError(f"access denied: {path} is outside workspace ({_workspace})")
    return _builtin_open(resolved, *args, **kwargs)
_builtins.open = _safe_open

# Guard shutil.rmtree
try:
    import shutil as _shutil
    _original_rmtree = _shutil.rmtree
    def _safe_rmtree(path, *args, **kwargs):
        resolved = os.path.realpath(str(path))
        if not resolved.startswith(_workspace + os.sep):
            raise PermissionError(f"cannot delete outside workspace: {path}")
        if resolved == _workspace:
            raise PermissionError("cannot delete workspace root")
        return _original_rmtree(resolved, *args, **kwargs)
    _shutil.rmtree = _safe_rmtree
except ImportError:
    pass

# Guard os.remove and os.rmdir
_original_remove = os.remove
_original_rmdir = os.rmdir
def _safe_remove(path, *args, **kwargs):
    resolved = os.path.realpath(os.path.join(_workspace, str(path)))
    if not resolved.startswith(_workspace + os.sep):
        raise PermissionError(f"cannot delete outside workspace: {path}")
    return _original_remove(resolved, *args, **kwargs)
def _safe_rmdir(path, *args, **kwargs):
    resolved = os.path.realpath(os.path.join(_workspace, str(path)))
    if not resolved.startswith(_workspace + os.sep):
        raise PermissionError(f"cannot delete outside workspace: {path}")
    return _original_rmdir(resolved, *args, **kwargs)
os.remove = _safe_remove
os.rmdir = _safe_rmdir

# Block os.system and subprocess — force use of call_tool() instead
os.system = lambda *a, **kw: (_ for _ in ()).throw(
    PermissionError("os.system is blocked — use call_tool() instead"))
sys.modules['subprocess'] = type(sys)('subprocess')
sys.modules['subprocess'].__dict__.update({
    'run': lambda *a, **kw: (_ for _ in ()).throw(PermissionError("subprocess is blocked — use call_tool() instead")),
    'Popen': lambda *a, **kw: (_ for _ in ()).throw(PermissionError("subprocess is blocked — use call_tool() instead")),
    'call': lambda *a, **kw: (_ for _ in ()).throw(PermissionError("subprocess is blocked — use call_tool() instead")),
    'check_output': lambda *a, **kw: (_ for _ in ()).throw(PermissionError("subprocess is blocked — use call_tool() instead")),
    'check_call': lambda *a, **kw: (_ for _ in ()).throw(PermissionError("subprocess is blocked — use call_tool() instead")),
})

# --- User code starts below ---
