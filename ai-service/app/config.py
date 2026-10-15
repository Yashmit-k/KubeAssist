import os
from pathlib import Path

from dotenv import load_dotenv

from app.paths import FAISS_DIR, SERVICE_DIR

load_dotenv(SERVICE_DIR.parent / ".env")
load_dotenv(SERVICE_DIR / ".env")


def _bool(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


class Settings:
    host: str = os.getenv("HOST", "0.0.0.0")
    port: int = int(os.getenv("PORT", "8000"))
    shared_token: str = os.getenv("AI_SHARED_TOKEN", "dev-shared-token-change-me")
    llm_api_key: str = os.getenv("LLM_API_KEY", "")
    llm_base_url: str = os.getenv("LLM_BASE_URL", "https://api.openai.com/v1")
    llm_model: str = os.getenv("LLM_MODEL", "gpt-4o-mini")
    llm_mock: bool = _bool("LLM_MOCK", True)
    rag_top_k: int = int(os.getenv("RAG_TOP_K", "5"))
    faiss_dir: Path = Path(os.getenv("FAISS_INDEX_DIR", str(FAISS_DIR)))

    @property
    def use_mock_llm(self) -> bool:
        return self.llm_mock or not self.llm_api_key


settings = Settings()
