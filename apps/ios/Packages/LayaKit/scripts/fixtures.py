"""Writes LayaKit's test fixtures from the Python reference, laya-coreml.

    uv run --no-project --with 'laya-coreml @ git+https://github.com/mizorewww/laya-coreml@4619e04' \
      python scripts/fixtures.py [--answers]

laya-coreml main, not PyPI 0.1.0: main clamps calibration temperatures to
[0.5, 5] (following upstream v0.3.5), which LayaKit does too.

Needs the checkpoints in .models (scripts/fetch-model.sh; --tokenizer is enough
for the multilingual one). The tests never run Python: they read what this
writes to Tests/LayaKitTests/Fixtures.

- tokenizer_ids.json  token ids per sample text, per checkpoint tokenizer
- sequences.json      laya's build_sequence (ids, markers) for every case
- answers-<name>.json (--answers: needs the whole checkpoint) what laya-coreml's
                      Agent answers for every case, with the unrounded
                      calibrated probabilities (CPU+GPU, as shipped)
"""

import json
import sys
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).parent))
from cases import ideacheck_cases, texts, validation_cases  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
MODELS = ROOT / ".models"
OUT = ROOT / "Tests/LayaKitTests/Fixtures"


def tokenizer_ids():
    from laya_coreml.tokenizer import Tokenizer

    out = {}
    for name in ("laya-typed-decisions-coreml", "laya-multilingual-coreml-ane"):
        tok = Tokenizer(MODELS / name / "tokenizer")
        out[name] = {
            "specials": {
                "cls": tok.cls_token_id,
                "sep": tok.sep_token_id,
                "pad": tok.pad_token_id,
                "mask": tok.mask_token_id,
                "mask_token": tok.mask_token,
            },
            "cases": [{"text": t, "ids": tok(t)["input_ids"]} for t in texts()],
        }
    return out


def all_cases():
    return [("validation", *c) for c in validation_cases()] + [("ideacheck", *c) for c in ideacheck_cases()]


def sequences(agent):
    out = []
    for group, name, state, questions in all_cases():
        items, _ = agent.prepare(state, questions)
        out.append(
            {
                "group": group,
                "name": name,
                "state": state,
                "questions": questions,
                "sequences": [{"ids": i["ids"], "markers": i["markers"], "qtype": i["qtype"]} for i in items],
            }
        )
    return out


def answers(agent):
    """system_one without its rounding, so the Swift side can be held to 1e-3."""
    from laya_coreml.common import temp_bucket
    from laya_coreml.inputs import collate_items

    out = []
    for group, name, state, questions in all_cases():
        items, internal = agent.prepare(state, questions)
        rows, kept = [], {}
        for qid, item, q in zip(questions, items, internal):
            try:
                batch = collate_items([item], agent.tok.pad_token_id, shape=agent.shape)
            except ValueError:
                continue  # longer than the graph (the ANE bundles take 96 tokens)
            kept[qid] = questions[qid]
            result = agent.predict(state, {qid: questions[qid]})["answers"]
            logits, _ = agent.forward(batch)
            k, qt = len(item["markers"]), item["qtype"]
            scale = agent.temperature_by_options.get(temp_bucket(qt, k), agent.temperature[qt])
            z = logits[0, :k] / scale
            p = np.exp(z - z.max())
            p /= p.sum()
            a = result[qid]
            selected = a.get("choice") if q["t"] == "choice" else str(int(p.argmax())) if q["t"] == "score" else ("true" if a["noul"] >= 0.5 else "false")
            rows.append({"id": qid, "selected": selected, "probabilities": [float(v) for v in p], "published": a})
        if rows:
            out.append({"group": group, "name": name, "state": state, "questions": kept, "answers": rows})
    return out


def prompter(name):
    """laya-coreml's input side alone: tokenizer and config, no Core ML."""
    from laya_coreml.prompt import PromptMixin
    from laya_coreml.tokenizer import Tokenizer

    p = PromptMixin()
    p.tok = Tokenizer(MODELS / name / "tokenizer")
    p.cfg = json.loads((MODELS / name / "rl_agent_config.json").read_text())
    return p


def write(name, value):
    (OUT / name).write_text(json.dumps(value, ensure_ascii=False, separators=(",", ":")))


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    write("tokenizer_ids.json", tokenizer_ids())
    write("sequences.json", sequences(prompter("laya-typed-decisions-coreml")))
    if "--answers" in sys.argv:
        import laya_coreml as laya

        for name in ("laya-typed-decisions-coreml", "laya-multilingual-coreml-ane"):
            if (MODELS / name / "model.mlpackage").exists():
                write("answers-%s.json" % name, answers(laya.load(str(MODELS / name))))
    print("wrote", ", ".join(p.name for p in sorted(OUT.glob("*.json"))))


if __name__ == "__main__":
    main()
