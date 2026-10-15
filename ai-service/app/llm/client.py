from __future__ import annotations

import json
from typing import Any

import httpx

from app.config import settings


async def complete_json(prompt: str) -> dict[str, Any] | None:
    if settings.use_mock_llm:
        return None
    url = settings.llm_base_url.rstrip("/") + "/chat/completions"
    headers = {"Authorization": f"Bearer {settings.llm_api_key}"}
    body = {
        "model": settings.llm_model,
        "temperature": 0.1,
        "response_format": {"type": "json_object"},
        "messages": [
            {"role": "system", "content": "Return only valid DiagnosisResponse JSON."},
            {"role": "user", "content": prompt},
        ],
    }
    async with httpx.AsyncClient(timeout=45.0) as client:
        resp = await client.post(url, headers=headers, json=body)
        resp.raise_for_status()
        content = resp.json()["choices"][0]["message"]["content"]
    return json.loads(content)
