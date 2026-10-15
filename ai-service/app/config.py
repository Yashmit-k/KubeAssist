import logging
import os

from dotenv import load_dotenv

from app.paths import SERVICE_DIR

load_dotenv(SERVICE_DIR.parent / ".env")
load_dotenv(SERVICE_DIR / ".env")

DEFAULT_TOKEN = "dev-shared-token-change-me"


def _bool(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


class Settings:
    host: str = os.getenv("HOST", "0.0.0.0")
    port: int = int(os.getenv("PORT", "8000"))
    shared_token: str = os.getenv("AI_SHARED_TOKEN", DEFAULT_TOKEN)
    # Any OpenAI-compatible chat completions endpoint works: OpenAI,
    # Azure OpenAI, OpenRouter, Groq, Together, vLLM, Ollama, LM Studio...
    llm_api_key: str = os.getenv("LLM_API_KEY", "")
    llm_base_url: str = os.getenv("LLM_BASE_URL", "https://api.openai.com/v1")
    llm_model: str = os.getenv("LLM_MODEL", "gpt-4o-mini")
    llm_timeout: float = float(os.getenv("LLM_TIMEOUT", "60"))
    llm_mock: bool = _bool("LLM_MOCK", True)
    # Local servers such as Ollama need no key; set LLM_REQUIRE_KEY=false.
    llm_require_key: bool = _bool("LLM_REQUIRE_KEY", True)
    rag_top_k: int = int(os.getenv("RAG_TOP_K", "5"))

    @property
    def use_mock_llm(self) -> bool:
        return self.llm_mock or (self.llm_require_key and not self.llm_api_key)


settings = Settings()

logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO"), format="%(asctime)s %(levelname)s %(name)s: %(message)s")
log = logging.getLogger("kubeassist.ai")

if settings.shared_token == DEFAULT_TOKEN:
    log.warning("AI_SHARED_TOKEN is the development default; set a real secret before deploying")
