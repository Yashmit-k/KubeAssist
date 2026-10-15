from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

import json
import os

import httpx

from app.paths import FIXTURES_DIR


def main() -> None:
    base = os.getenv("AI_BASE_URL", "http://127.0.0.1:8000")
    token = os.getenv("AI_SHARED_TOKEN", "dev-shared-token-change-me")
    name = sys.argv[1] if len(sys.argv) > 1 else "crashloop-app.json"
    path = FIXTURES_DIR / name
    snapshot = json.loads(path.read_text(encoding="utf-8"))
    r = httpx.post(
        f"{base}/ai/troubleshoot",
        json=snapshot,
        headers={"Authorization": f"Bearer {token}"},
        timeout=60.0,
    )
    print(r.status_code)
    print(json.dumps(r.json(), indent=2))
    r.raise_for_status()


if __name__ == "__main__":
    main()
