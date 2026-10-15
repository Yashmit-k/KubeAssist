"""The real-LLM path, exercised against a fake OpenAI-compatible server
(httpx.MockTransport), so no API key or network is needed."""
from __future__ import annotations

import asyncio
import json

import httpx
import pytest

from app.config import settings
from app.llm import client as llm_client
from app.llm.client import LLMError, extract_json
from app.pipeline.troubleshoot import run_troubleshoot
from app.pipeline.validator import is_write_command
from app.schema_util import validate_diagnosis


@pytest.fixture
def live_llm(monkeypatch):
    """Switch settings to real-LLM mode and route httpx to a handler the
    test supplies."""
    monkeypatch.setattr(settings, "llm_mock", False)
    monkeypatch.setattr(settings, "llm_api_key", "test-key")
    calls: list[dict] = []

    def install(handler):
        def wrapped(request: httpx.Request) -> httpx.Response:
            calls.append(json.loads(request.content))
            return handler(request)

        transport = httpx.MockTransport(wrapped)
        real = httpx.AsyncClient

        class Patched(real):  # type: ignore[misc, valid-type]
            def __init__(self, *a, **kw):
                kw["transport"] = transport
                super().__init__(*a, **kw)

        monkeypatch.setattr(llm_client.httpx, "AsyncClient", Patched)
        return calls

    return install


def chat(content: str) -> httpx.Response:
    return httpx.Response(200, json={"choices": [{"message": {"content": content}}]})


def good_answer(url: str) -> dict:
    return {
        "issueSummary": "The app container exits on startup.",
        "observedEvidence": ["Container \"app\" waiting: CrashLoopBackOff (restarts: 5)"],
        "probableRootCause": "Missing configuration makes the process exit.",
        "reasoning": "Previous logs show a fatal error [1].",
        "confidenceScore": 0.8,
        "resolutionSteps": [
            {"order": 1, "description": "Read previous logs", "command": "kubectl logs crashloop-app -n kubeassist-lab --previous", "isWriteAction": False},
            {"order": 2, "description": "Restart after fixing", "command": "kubectl rollout restart deployment/crashloop-app -n kubeassist-lab", "isWriteAction": False},
        ],
        "verificationCommands": ["kubectl get pods -n kubeassist-lab"],
        "rollbackGuidance": "kubectl rollout undo deployment/crashloop-app",
        "documentationReferences": [
            {"title": "x", "url": url},
            {"title": "invented", "url": "https://example.com/made-up"},
        ],
        "uncertaintyNotes": [],
        "failureType": "OOMKilled",  # must be ignored
        "extraField": "must be dropped",
    }


def test_full_rag_path(snapshots, built_index, live_llm):
    snap = snapshots["crashloop-app"]
    hits = built_index.search(snap["detector"]["searchTerms"], "CrashLoopBackOff", top_k=settings.rag_top_k)
    calls = live_llm(lambda req: chat("```json\n" + json.dumps(good_answer(hits[0].url)) + "\n```"))

    out = asyncio.run(run_troubleshoot(snap, built_index))

    assert out["mode"] == "full_rag"
    assert out["failureType"] == "CrashLoopBackOff"  # detector wins
    assert "extraField" not in out
    assert not validate_diagnosis(out)
    urls = [r["url"] for r in out["documentationReferences"]]
    assert "https://example.com/made-up" not in urls  # invented citation dropped
    restart = out["resolutionSteps"][1]
    assert restart["isWriteAction"] is True  # mislabelled write command corrected
    # The prompt carried the evidence, the docs and the real pod name.
    user_msg = calls[0]["messages"][1]["content"]
    assert "crashloop-app" in user_msg and hits[0].url in user_msg


def test_llm_http_error_falls_back_to_playbook(snapshots, built_index, live_llm):
    live_llm(lambda req: httpx.Response(503, text="overloaded"))
    out = asyncio.run(run_troubleshoot(snapshots["oom-failure"], built_index))
    assert out["mode"] == "mock_llm"
    assert out["failureType"] == "OOMKilled"
    assert any("could not be reached" in n for n in out["uncertaintyNotes"])
    assert not validate_diagnosis(out)


def test_ungrounded_llm_answer_is_rejected(snapshots, built_index, live_llm):
    bad = good_answer("https://kubernetes.io/docs/concepts/containers/images/")
    bad["observedEvidence"] = ["The database server zzqx-primary-77 rejected credentials at 03:14"]
    live_llm(lambda req: chat(json.dumps(bad)))
    out = asyncio.run(run_troubleshoot(snapshots["crashloop-app"], built_index))
    assert out["mode"] == "mock_llm"
    assert any("failed grounding" in n for n in out["uncertaintyNotes"])


def test_response_format_unsupported_is_retried(snapshots, built_index, live_llm):
    hits = built_index.search([], "ImagePullBackOff", top_k=settings.rag_top_k)
    ans = good_answer(hits[0].url)
    ans["observedEvidence"] = ["Container \"app\" waiting: ImagePullBackOff"]

    def handler(req: httpx.Request) -> httpx.Response:
        if "response_format" in json.loads(req.content):
            return httpx.Response(400, text="unknown field response_format")
        return chat(json.dumps(ans))

    calls = live_llm(handler)
    out = asyncio.run(run_troubleshoot(snapshots["image-pull-failure"], built_index))
    assert len(calls) == 2
    assert out["failureType"] == "ImagePullBackOff"


def test_extract_json_variants():
    assert extract_json('{"a": 1}') == {"a": 1}
    assert extract_json('Sure!\n```json\n{"a": 2}\n```') == {"a": 2}
    assert extract_json('Here you go: {"a": 3} hope it helps') == {"a": 3}
    with pytest.raises(LLMError):
        extract_json("no json here")


@pytest.mark.parametrize(
    "cmd,write",
    [
        ("kubectl get pods -n x", False),
        ("kubectl describe pod a", False),
        ("kubectl logs a --previous", False),
        ("kubectl delete pod a", True),
        ("kubectl -n x scale deploy/a --replicas=0", True),
        ("kubectl rollout restart deployment/a", True),
        ("kubectl rollout status deployment/a", False),
        ("kubectl set image deploy/a a=b", True),
        ("helm upgrade a ./chart", True),
    ],
)
def test_write_command_detection(cmd, write):
    assert is_write_command(cmd) is write
