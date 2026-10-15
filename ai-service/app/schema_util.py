from __future__ import annotations

import json
from functools import lru_cache
from typing import Any

from jsonschema import Draft202012Validator, FormatChecker

from app.paths import DIAGNOSIS_SCHEMA, SNAPSHOT_SCHEMA


@lru_cache(maxsize=2)
def _load(path_str: str) -> dict[str, Any]:
    with open(path_str, encoding="utf-8") as f:
        return json.load(f)


@lru_cache(maxsize=2)
def _validator(path_str: str) -> Draft202012Validator:
    return Draft202012Validator(_load(path_str), format_checker=FormatChecker())


def validate_snapshot(doc: dict[str, Any]) -> list[str]:
    return [e.message for e in _validator(str(SNAPSHOT_SCHEMA)).iter_errors(doc)]


def validate_diagnosis(doc: dict[str, Any]) -> list[str]:
    return [e.message for e in _validator(str(DIAGNOSIS_SCHEMA)).iter_errors(doc)]
