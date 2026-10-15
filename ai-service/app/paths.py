from pathlib import Path

APP_DIR = Path(__file__).resolve().parent
SERVICE_DIR = APP_DIR.parent
REPO_ROOT = SERVICE_DIR.parent
SCHEMAS_DIR = REPO_ROOT / "schemas"
SNAPSHOT_SCHEMA = SCHEMAS_DIR / "diagnostic-snapshot.schema.json"
DIAGNOSIS_SCHEMA = SCHEMAS_DIR / "diagnosis.schema.json"
RAW_DOCS_DIR = SERVICE_DIR / "data" / "raw_docs"
FAISS_DIR = SERVICE_DIR / "data" / "faiss"
FIXTURES_DIR = SERVICE_DIR / "tests" / "fixtures" / "snapshots"
