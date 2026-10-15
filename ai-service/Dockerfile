# Build from the REPOSITORY ROOT so the shared schemas are in context:
#   docker build -f ai-service/Dockerfile -t kubeassist-ai .
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app \
    SCHEMAS_DIR=/app/schemas HOST=0.0.0.0 PORT=8000

WORKDIR /app
COPY ai-service/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY schemas ./schemas
COPY ai-service/app ./app
COPY ai-service/data/raw_docs ./data/raw_docs
COPY ai-service/scripts ./scripts

# Build the retrieval index at image build time so startup is instant.
RUN python scripts/build_index.py \
    && useradd --system --uid 10001 app && chown -R app /app
USER 10001

EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health')"
CMD ["sh", "-c", "uvicorn app.main:app --host ${HOST} --port ${PORT}"]
