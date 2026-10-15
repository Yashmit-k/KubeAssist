"""Go sends detector.fix (exact values + commands built from the
evidence). Both the playbook answer and the LLM answer must end with
those concrete commands instead of template placeholders."""

import asyncio
import copy
import json

from app.config import settings
from app.pipeline.formatter import format_evidence
from app.pipeline.troubleshoot import run_troubleshoot
from app.schema_util import validate_diagnosis
from tests.test_llm_path import chat, live_llm  # noqa: F401  (fixture)

OOM_FIX = {
    "facts": {"owner": "deployment/oom-failure", "container": "application", "memoryLimit": "32Mi", "suggestedMemoryLimit": "64Mi"},
    "steps": [{
        "description": "Raise container \"application\"'s memory limit from 32Mi to 64Mi (double).",
        "command": "kubectl set resources deployment/oom-failure -n kubeassist-lab -c application --limits=memory=64Mi",
        "isWriteAction": True,
    }],
}


def oom_with_fix(snapshots):
    snap = copy.deepcopy(snapshots["oom-failure"])
    snap["detector"]["fix"] = copy.deepcopy(OOM_FIX)
    return snap


def commands(diagnosis):
    return [s.get("command") or "" for s in diagnosis["resolutionSteps"]]


def test_playbook_answer_uses_concrete_fix(snapshots, built_index):
    out = asyncio.run(run_troubleshoot(oom_with_fix(snapshots), built_index))
    cmds = commands(out)
    assert not validate_diagnosis(out)
    assert OOM_FIX["steps"][0]["command"] in cmds
    assert not any("<new-limit>" in c for c in cmds)
    assert any(c.startswith("kubectl describe pod") for c in cmds)  # read-only investigation kept
    assert [s["order"] for s in out["resolutionSteps"]] == list(range(1, len(cmds) + 1))
    assert any("built from this pod's evidence" in n for n in out["uncertaintyNotes"])


def test_playbook_without_fix_is_unchanged(snapshots, built_index):
    out = asyncio.run(run_troubleshoot(snapshots["oom-failure"], built_index))
    assert any("<new-limit>" in c for c in commands(out))


def test_evidence_shows_fix_facts_and_commands(snapshots):
    text = format_evidence(oom_with_fix(snapshots))
    assert "FIX FACTS" in text and "suggestedMemoryLimit: 64Mi" in text
    assert "$ kubectl set resources deployment/oom-failure" in text


def test_llm_placeholder_fix_is_replaced(snapshots, built_index, live_llm):  # noqa: F811
    snap = oom_with_fix(snapshots)
    answer = {
        "issueSummary": "The container ran out of memory.",
        "observedEvidence": [snap["detector"]["evidence"][0]],
        "probableRootCause": "Memory use exceeds the limit.",
        "reasoning": "OOMKilled with a 32Mi limit.",
        "confidenceScore": 0.8,
        "resolutionSteps": [
            {"order": 1, "description": "Inspect", "command": "kubectl describe pod x -n kubeassist-lab", "isWriteAction": False},
            {"order": 2, "description": "Raise the limit", "command": "kubectl set resources deployment/oom-failure -c application --limits=memory=<new-limit>", "isWriteAction": True},
        ],
        "verificationCommands": [],
        "rollbackGuidance": "Restore the old limit.",
        "documentationReferences": [],
        "uncertaintyNotes": [],
    }
    calls = live_llm(lambda req: chat(json.dumps(answer)))
    out = asyncio.run(run_troubleshoot(snap, built_index))
    cmds = commands(out)
    assert out["mode"] == "full_rag", out["uncertaintyNotes"]
    assert OOM_FIX["steps"][0]["command"] in cmds
    assert not any("<new-limit>" in c for c in cmds)
    # The prompt told the model about the exact values.
    assert "suggestedMemoryLimit: 64Mi" in calls[0]["messages"][1]["content"]
    assert "Never write a placeholder" in calls[0]["messages"][0]["content"]


def test_llm_answer_using_fix_is_kept(snapshots, built_index, live_llm):  # noqa: F811
    snap = oom_with_fix(snapshots)
    fix_cmd = OOM_FIX["steps"][0]["command"]
    answer = {
        "issueSummary": "Out of memory.", "observedEvidence": [snap["detector"]["evidence"][0]],
        "probableRootCause": "Limit too low.", "reasoning": "r", "confidenceScore": 0.8,
        "resolutionSteps": [{"order": 1, "description": "Double the limit to 64Mi", "command": fix_cmd, "isWriteAction": True}],
        "verificationCommands": [], "rollbackGuidance": "r", "documentationReferences": [], "uncertaintyNotes": [],
    }
    live_llm(lambda req: chat(json.dumps(answer)))
    out = asyncio.run(run_troubleshoot(snap, built_index))
    assert commands(out) == [fix_cmd]
    assert settings.llm_mock is False
