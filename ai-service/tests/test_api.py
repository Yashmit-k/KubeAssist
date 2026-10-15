import json

from fastapi.testclient import TestClient

from app.config import settings
from app.main import app
from app.paths import FIXTURES_DIR


def test_health_and_simulated_go_post():
    headers = {"Authorization": f"Bearer {settings.shared_token}"}
    with TestClient(app) as client:
        h = client.get("/health")
        assert h.status_code == 200
        body = json.loads((FIXTURES_DIR / "crashloop-app.json").read_text(encoding="utf-8"))
        r = client.post("/ai/troubleshoot", json=body, headers=headers)
        assert r.status_code == 200, r.text
        data = r.json()
        assert data["failureType"] == "CrashLoopBackOff"
        assert data["confidenceScore"] >= 0
        assert data["resolutionSteps"]


def test_rejects_bad_snapshot():
    headers = {"Authorization": f"Bearer {settings.shared_token}"}
    with TestClient(app) as client:
        r = client.post("/ai/troubleshoot", json={"schemaVersion": "1.1.0"}, headers=headers)
        assert r.status_code == 400
