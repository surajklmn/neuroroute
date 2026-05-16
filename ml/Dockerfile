# ──────────────────────────────────────────────────
#  NeuroRoute ML Service — Docker Image
# ──────────────────────────────────────────────────

FROM python:3.11-slim

WORKDIR /app

# Install dependencies first (layer caching)
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY train.py .
COPY server.py .

# Pre-create data directory
RUN mkdir -p /app/data

EXPOSE 8050

# Run the prediction server
CMD ["uvicorn", "server:app", "--host", "0.0.0.0", "--port", "8050", "--log-level", "info"]
