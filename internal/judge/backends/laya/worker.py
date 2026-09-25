"""ideacheck's Laya worker: one process, the model loaded once, one JSON object per line.

    python -c "$worker" serve MODEL [DTYPE]   answer requests on stdin until EOF or idle
    python -c "$worker" download MODEL        fetch the checkpoint, reporting progress

Request:  {"id": 1, "state": ..., "questions": {...}}   (laya_mlx's own shapes)
Reply:    {"id": 1, "model": ..., "answers": {...}, "usage": {...}, "at_limit": [qid...]}
      or  {"id": 1, "error": "..."}
The first line out is {"ready": true, ...} or {"error": "..."}; at_limit names the
questions whose sequence filled the context, so the state was (or was about to be) cut.
"""

import json
import os
import select
import sys
import threading
import time

IDLE_SECONDS = 600  # free the memory when ideacheck has been quiet this long; it starts another
PATTERNS = ["model.safetensors", "rl_agent_config.json", "encoder/config.json", "tokenizer/*", "mlx_config.json"]


def out(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def fail(exc):
    return "%s: %s" % (type(exc).__name__, exc)


def lines(idle):
    """Yield complete lines from fd 0, or stop after idle seconds without one.

    select() is on the raw descriptor, so nothing may sit in a Python-level buffer.
    """
    buf = b""
    while True:
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            if line.strip():
                yield line.decode("utf-8")
        ready, _, _ = select.select([0], [], [], idle)
        if not ready:
            return
        chunk = os.read(0, 1 << 16)
        if not chunk:
            return
        buf += chunk


def at_limit(agent, state, questions):
    """Questions whose sequence is as long as the model allows: the state was cut to fit."""
    max_len = agent.cfg.get("max_len", 512)
    try:
        items, _ = agent.prepare(state, questions)
    except Exception:
        return []
    return [qid for qid, item in zip(questions, items) if len(item["ids"]) >= max_len]


def serve(model, dtype):
    import laya_mlx as laya

    started = time.time()
    try:
        agent = laya.load(model, dtype=dtype)
    except Exception as exc:
        out({"error": fail(exc)})
        return 1
    out({"ready": True, "model": model, "load_ms": int((time.time() - started) * 1000), "max_len": agent.cfg.get("max_len", 512)})
    for line in lines(IDLE_SECONDS):
        try:
            req = json.loads(line)
        except ValueError as exc:
            out({"error": fail(exc)})
            continue
        rid = req.get("id")
        try:
            res = agent.predict(req["state"], req["questions"])
            res["id"], res["model"] = rid, model
            res["at_limit"] = at_limit(agent, req["state"], req["questions"])
            out(res)
        except Exception as exc:
            out({"id": rid, "error": fail(exc)})
    return 0


def repo_size(model):
    """Bytes of the checkpoint files, from the Hub; 0 when it cannot say."""
    from huggingface_hub import HfApi
    from huggingface_hub.utils import filter_repo_objects

    info = HfApi().model_info(model, files_metadata=True)
    names = set(filter_repo_objects([s.rfilename for s in info.siblings], allow_patterns=PATTERNS))
    return sum(s.size or 0 for s in info.siblings if s.rfilename in names)


def local_size(model):
    """Bytes downloaded so far: the repo's blobs, partial files included."""
    from huggingface_hub.constants import HF_HUB_CACHE
    from huggingface_hub.file_download import repo_folder_name

    blobs = os.path.join(HF_HUB_CACHE, repo_folder_name(repo_id=model, repo_type="model"), "blobs")
    total = 0
    for root, _, files in os.walk(blobs):
        for name in files:
            try:
                total += os.stat(os.path.join(root, name)).st_size
            except OSError:
                pass
    return total


def download(model):
    from huggingface_hub import snapshot_download

    if os.path.isdir(os.path.expanduser(model)):
        out({"status": "done", "done": 0, "total": 0})
        return 0
    try:
        total = repo_size(model)
    except Exception as exc:
        out({"error": fail(exc)})
        return 1
    status = "downloading " + model
    out({"status": status, "done": local_size(model), "total": total})
    stop = threading.Event()

    def report():
        while not stop.wait(0.5):
            out({"status": status, "done": min(local_size(model), total), "total": total})

    threading.Thread(target=report, daemon=True).start()
    try:
        snapshot_download(model, allow_patterns=PATTERNS)
    except Exception as exc:
        stop.set()
        out({"error": fail(exc)})
        return 1
    stop.set()
    out({"status": "done", "done": total, "total": total})
    return 0


def main(argv):
    if len(argv) >= 2 and argv[0] == "serve":
        return serve(argv[1], argv[2] if len(argv) > 2 else "float16")
    if len(argv) == 2 and argv[0] == "download":
        return download(argv[1])
    out({"error": "usage: serve MODEL [DTYPE] | download MODEL"})
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
