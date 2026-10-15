import asyncio

import pytest

from app.pipeline.troubleshoot import run_troubleshoot
from app.schema_util import validate_diagnosis


@pytest.mark.parametrize("key", ["crashloop-app", "oom-failure", "image-pull-failure", "service-selector-failure"])
def test_troubleshoot_echoes_type(snapshots, built_index, key):
    snap = snapshots[key]
    out = asyncio.run(run_troubleshoot(snap, built_index))
    assert out["failureType"] == snap["detector"]["failureType"]
    assert out["schemaVersion"] == "1.1.0"
    assert out["mode"] in {"mock_llm", "full_rag", "detector_only_stub"}
    assert not validate_diagnosis(out)
    for ref in out["documentationReferences"]:
        assert str(ref["url"]).startswith("https://")
