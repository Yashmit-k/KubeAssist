"""The read-only tools the Go backend executes for chat. Their schemas
are what the LLM sees; their result shapes are defined in Go
(backend-go/internal/api/chat_tools.go) and composed into answers by
app.chat.compose when no LLM is configured."""

from __future__ import annotations

from typing import Any

_NS = {"type": "string", "description": "Kubernetes namespace. Omit for all namespaces."}
_POD = {"type": "string", "description": "Pod name, or a unique part of it (e.g. 'oom-failure')."}

TOOLS: list[dict[str, Any]] = [
    {
        "name": "scan",
        "description": "Health scan: find every unhealthy pod, group them by root cause, and return one exact fix per cause. Use for 'what's broken?' questions.",
        "parameters": {"type": "object", "properties": {"namespace": _NS}, "additionalProperties": False},
    },
    {
        "name": "diagnose_pod",
        "description": "Classify one pod's failure from its status, events and logs, with the evidence and exact fix commands.",
        "parameters": {
            "type": "object",
            "properties": {"name": _POD, "namespace": _NS},
            "required": ["name"],
            "additionalProperties": False,
        },
    },
    {
        "name": "get_logs",
        "description": "Recent (redacted) log lines of a pod's container. previous=true reads the last crashed instance.",
        "parameters": {
            "type": "object",
            "properties": {
                "name": _POD,
                "namespace": _NS,
                "container": {"type": "string"},
                "previous": {"type": "boolean"},
            },
            "required": ["name"],
            "additionalProperties": False,
        },
    },
    {
        "name": "list_pods",
        "description": "List pods with their status, readiness and restarts, unhealthy first.",
        "parameters": {"type": "object", "properties": {"namespace": _NS}, "additionalProperties": False},
    },
    {
        "name": "list_namespaces",
        "description": "List the cluster's namespaces.",
        "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
    },
]

TOOL_NAMES = {t["name"] for t in TOOLS}


def openai_tools() -> list[dict[str, Any]]:
    return [{"type": "function", "function": t} for t in TOOLS]
