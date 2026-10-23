import asyncio
import json

import httpx
import pytest
from fastapi.testclient import TestClient

from app.chat import rules
from app.chat.compose import compose
from app.chat.service import chat_step
from app.config import settings
from app.main import app
from tests.test_llm_path import live_llm  # noqa: F401  (fixture)


def user(text):
    return {"messages": [{"role": "user", "content": text}]}


@pytest.mark.parametrize(
    "question,want",
    [
        ("What's broken?", {"name": "scan", "arguments": {}}),
        ("what's wrong in kubeassist-lab", {"name": "scan", "arguments": {"namespace": "kubeassist-lab"}}),
        ("Why is oom-failure crashing?", {"name": "diagnose_pod", "arguments": {"name": "oom-failure"}}),
        ("why is oom-failure in kubeassist-lab failing", {"name": "diagnose_pod", "arguments": {"namespace": "kubeassist-lab", "name": "oom-failure"}}),
        ("show logs for crashloop-app", {"name": "get_logs", "arguments": {"name": "crashloop-app"}}),
        ("logs of crashloop-app before it crashed", {"name": "get_logs", "arguments": {"name": "crashloop-app", "previous": True}}),
        ("list pods in namespace kube-system", {"name": "list_pods", "arguments": {"namespace": "kube-system"}}),
        ("which namespaces are there?", {"name": "list_namespaces", "arguments": {}}),
        ("is anything failing in my cluster", {"name": "scan", "arguments": {}}),
    ],
)
def test_rules_plan(question, want):
    assert rules.plan(question) == want


def test_rules_unknown_question_gets_help():
    assert rules.plan("hello there") is None
    out = asyncio.run(chat_step(user("hello there")))
    assert out["type"] == "answer" and "What's broken?" in out["content"] and out["mode"] == "rules"


SCAN = {
    "namespace": "kubeassist-lab", "totalPods": 11,
    "groups": [{
        "title": 'ConfigMap "application-config" is missing key "database-url"', "failureType": "CreateContainerConfigError",
        "pods": [{"namespace": "kubeassist-lab", "name": "missing-config-app-1", "status": "CreateContainerConfigError"}],
        "fix": {"steps": [
            {"description": "Read it", "command": "kubectl describe pod x", "isWriteAction": False},
            {"description": "Add the key", "command": "kubectl patch configmap application-config -n kubeassist-lab --type merge -p '{\"data\":{\"database-url\":\"<value>\"}}'", "isWriteAction": True},
        ]},
    }],
}


def test_mock_chat_round_trip():
    first = asyncio.run(chat_step(user("what's wrong in kubeassist-lab?")))
    assert first == {"type": "tool_calls", "toolCalls": [{"id": "rule_1", "name": "scan", "arguments": {"namespace": "kubeassist-lab"}}]}
    second = asyncio.run(chat_step({**user("what's wrong in kubeassist-lab?"), "toolResults": [{**first["toolCalls"][0], "result": SCAN}]}))
    text = second["content"]
    assert second["type"] == "answer" and second["mode"] == "rules"
    assert "1 unhealthy pod" in text and "missing key" in text
    assert "```bash\nkubectl patch configmap application-config" in text
    assert "kubectl describe pod x" not in text  # scan summaries show the fix commands only


def test_compose_shapes():
    diag = compose("diagnose_pod", {
        "namespace": "lab", "name": "oom-1", "failureType": "OOMKilled", "rootCause": "memory limit 32Mi is too low",
        "evidence": ["Container \"app\" was OOMKilled"], "fix": {"steps": [{"description": "Raise it", "command": "kubectl set resources x --limits=memory=64Mi", "isWriteAction": True}]},
    })
    assert "**lab/oom-1**: out of memory (`OOMKilled`)" in diag and "--limits=memory=64Mi" in diag
    assert "```log\nfatal: db down\n```" in compose("get_logs", {"namespace": "lab", "name": "p", "container": "c", "logs": "fatal: db down", "lines": 1})
    assert "Did you mean one of: `oom-a`, `oom-b`" in compose("diagnose_pod", {"error": "Several pods match \"oom\".", "candidates": ["oom-a", "oom-b"]})
    assert "no problems found" in compose("scan", {"totalPods": 3, "groups": []})


def tool_call_msg(name, args):
    return {"choices": [{"message": {"content": None, "tool_calls": [{"id": "c1", "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}]}}]}


def test_llm_chooses_tools_then_answers(live_llm):  # noqa: F811
    replies = iter([
        httpx.Response(200, json=tool_call_msg("diagnose_pod", {"name": "oom-failure"})),
        httpx.Response(200, json={"choices": [{"message": {"content": "It's OOMKilled. Run:\n```bash\nkubectl set resources ...\n```"}}]}),
    ])
    calls = live_llm(lambda req: next(replies))
    first = asyncio.run(chat_step(user("why is oom-failure crashing?")))
    assert first == {"type": "tool_calls", "toolCalls": [{"id": "c1", "name": "diagnose_pod", "arguments": {"name": "oom-failure"}}]}
    assert {t["function"]["name"] for t in calls[0]["tools"]} >= {"scan", "diagnose_pod", "get_logs"}
    assert "READ-ONLY" in calls[0]["messages"][0]["content"]

    second = asyncio.run(chat_step({**user("why is oom-failure crashing?"),
                                    "toolResults": [{**first["toolCalls"][0], "result": {"failureType": "OOMKilled"}}]}))
    assert second["type"] == "answer" and second["mode"] == "llm" and "OOMKilled" in second["content"]
    # The executed call and its result were replayed to the model.
    replay = calls[1]["messages"]
    assert replay[-2]["tool_calls"][0]["function"]["name"] == "diagnose_pod"
    assert replay[-1]["role"] == "tool" and "OOMKilled" in replay[-1]["content"]


def test_llm_unknown_tools_are_ignored_and_failures_fall_back(live_llm):  # noqa: F811
    live_llm(lambda req: httpx.Response(200, json=tool_call_msg("delete_pod", {"name": "x"})))
    out = asyncio.run(chat_step(user("hello")))
    assert out["type"] == "answer" and out["mode"] == "rules"  # no allowed tool, no content -> rule-based

    live_llm(lambda req: httpx.Response(500, text="boom"))
    out = asyncio.run(chat_step(user("what's broken?")))
    assert out == {"type": "tool_calls", "toolCalls": [{"id": "rule_1", "name": "scan", "arguments": {}}]}


def test_chat_endpoint_validates():
    headers = {"Authorization": f"Bearer {settings.shared_token}"}
    with TestClient(app) as client:
        ok = client.post("/ai/chat", json=user("what's broken?"), headers=headers)
        assert ok.status_code == 200 and ok.json()["type"] == "tool_calls"
        for bad in ({}, {"messages": []}, {"messages": [{"role": "system", "content": "x"}]},
                    {"messages": [{"role": "user", "content": "a"}, {"role": "assistant", "content": "b"}]}):
            assert client.post("/ai/chat", json=bad, headers=headers).status_code == 400
        assert client.post("/ai/chat", json=user("x")).status_code == 401
