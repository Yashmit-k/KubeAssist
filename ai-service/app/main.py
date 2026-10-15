from contextlib import asynccontextmanager

from fastapi import FastAPI

from app.api.routes import router
from app.paths import FAISS_DIR
from app.rag.index import build_index
from app.state import retriever


@asynccontextmanager
async def lifespan(_: FastAPI):
    if not (FAISS_DIR / "vectors.npy").exists():
        build_index()
    retriever.load()
    yield


app = FastAPI(title="KubeAssist AI", version="1.1.0", lifespan=lifespan)
app.include_router(router)
