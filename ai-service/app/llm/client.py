from __future__ import annotations

import json
import logging
import re
from typing import Any

import httpx

from app.config import settings

log = logging.getLogger("kubeassist.ai.llm")

_FENCE_RE = re.compile(r"```(?:json)?\s*(\{.*\})\s*```", re.S)


class LLMError(RuntimeError):
    """The LLM call failed or returned something that isn't a JSON object."""


def extract_json(content: str) -> dict[str, Any]:
    """Parse a JSON object from model output, tolerating ```json fences
    and leading/trailing prose that some providers add even when asked
    not to."""
    content = content.strip()
    candidates = [content]
    if m := _FENCE_RE.search(content):
        candidates.insert(0, m.group(1))
    start, end = content.find("{"), content.rfind("}")
    if start != -1 and end > start:
        candidates.append(content[start : end + 1])
    for c in candidates:
        try:
            obj = json.loads(c)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict):
            return obj
    raise LLMError("model output was not a JSON object")


async def complete_json(system: str, prompt: str) -> dict[str, Any] | None:
    """Call an OpenAI-compatible /chat/completions endpoint and return the
    parsed JSON object, or None in mock mode. Raises LLMError on failure;
    the pipeline turns that into a playbook answer with an uncertainty
    note rather than an HTTP error."""
    if settings.use_mock_llm:
        return None

    url = settings.llm_base_url.rstrip("/") + "/chat/completions"
    headers = {"Content-Type": "application/json"}
    if settings.llm_api_key:
        headers["Authorization"] = f"Bearer {settings.llm_api_key}"
    body: dict[str, Any] = {
        "model": settings.llm_model,
        "temperature": 0.1,
        "response_format": {"type": "json_object"},
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": prompt},
        ],
    }

    async with httpx.AsyncClient(timeout=settings.llm_timeout) as client:
        try:
            resp = await client.post(url, headers=headers, json=body)
            if resp.status_code == 400 and "response_format" in resp.text:
                # Some OpenAI-compatible servers don't support JSON mode;
                # the prompt already demands JSON, so retry without it.
                body.pop("response_format")
                resp = await client.post(url, headers=headers, json=body)
            resp.raise_for_status()
            content = resp.json()["choices"][0]["message"]["content"]
        except httpx.HTTPStatusError as e:
            raise LLMError(f"LLM returned HTTP {e.response.status_code}: {e.response.text[:200]}") from e
        except (httpx.HTTPError, KeyError, IndexError, TypeError, ValueError) as e:
            raise LLMError(f"LLM request failed: {e}") from e

    if not isinstance(content, str):
        raise LLMError("LLM response had no text content")
    return extract_json(content)
