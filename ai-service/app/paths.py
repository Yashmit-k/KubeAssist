import os
from pathlib import Path

APP_DIR = Path(__file__).resolve().parent
SERVICE_DIR = APP_DIR.parent
REPO_ROOT = SERVICE_DIR.parent


def _schemas_dir() -> Path:
    """Shared JSON Schemas live at the monorepo root; the Docker image
    copies them to /app/schemas. SCHEMAS_DIR overrides both."""
    if env := os.getenv("SCHEMAS_DIR"):
        return Path(env)
    for candidate in (REPO_ROOT / "schemas", SERVICE_DIR / "schemas"):
        if candidate.is_dir():
            return candidate
    return REPO_ROOT / "schemas"


SCHEMAS_DIR = _schemas_dir()
SNAPSHOT_SCHEMA = SCHEMAS_DIR / "diagnostic-snapshot.schema.json"
DIAGNOSIS_SCHEMA = SCHEMAS_DIR / "diagnosis.schema.json"
RAW_DOCS_DIR = SERVICE_DIR / "data" / "raw_docs"
# Vector index (NumPy inner-product over L2-normalized vectors). The
# FAISS_INDEX_DIR name is kept for backwards compatibility.
INDEX_DIR = Path(os.getenv("INDEX_DIR") or os.getenv("FAISS_INDEX_DIR") or SERVICE_DIR / "data" / "index")
FAISS_DIR = INDEX_DIR
FIXTURES_DIR = SERVICE_DIR / "tests" / "fixtures" / "snapshots"
