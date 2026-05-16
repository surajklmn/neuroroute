"""
──────────────────────────────────────────────────────────────
 NeuroRoute — ML Prediction Server

 A FastAPI microservice that serves predictions from the trained
 RandomForestClassifier. The Go gateway calls POST /predict with
 request metadata and receives a label (0=light, 1=heavy).

 Features:
   • Loads model.pkl at startup (or uses rule-based fallback)
   • Sub-5ms prediction latency target
   • Health check endpoint
   • Structured logging

 Usage:
   uvicorn server:app --host 0.0.0.0 --port 8050
──────────────────────────────────────────────────────────────
"""

import hashlib
import logging
import os
import time
from contextlib import asynccontextmanager

import joblib
import numpy as np
from fastapi import FastAPI
from pydantic import BaseModel

# ── Logging ──────────────────────────────────────────────────

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s │ %(levelname)-7s │ %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("neuroroute-ml")

# ── Model State ──────────────────────────────────────────────

model = None
model_path = os.getenv("MODEL_PATH", "/app/model.pkl")
using_fallback = True


def load_model():
    """Attempt to load the trained model from disk."""
    global model, using_fallback

    if os.path.exists(model_path):
        try:
            model = joblib.load(model_path)
            using_fallback = False
            logger.info(f"✅ Model loaded from {model_path}")
            return True
        except Exception as e:
            logger.error(f"❌ Failed to load model: {e}")
            model = None
            using_fallback = True
            return False
    else:
        logger.warning(f"⚠️  Model not found at {model_path} — using rule-based fallback")
        using_fallback = True
        return False


# ── Schemas ──────────────────────────────────────────────────

class PredictRequest(BaseModel):
    url_path: str
    method: str
    content_length: int = 0
    is_heavy_query: int = 0


class PredictResponse(BaseModel):
    label: int
    confidence: float = 1.0
    mode: str = "model"


class HealthResponse(BaseModel):
    status: str
    model_loaded: bool
    mode: str


# ── App ──────────────────────────────────────────────────────

@asynccontextmanager
async def lifespan(app: FastAPI):
    """Load model on startup."""
    load_model()
    yield


app = FastAPI(
    title="NeuroRoute ML Service",
    description="Predicts request weight for intelligent load balancing",
    version="1.0.0",
    lifespan=lifespan,
)


# ── Encoding helpers ─────────────────────────────────────────

def encode_path(path: str) -> int:
    """Deterministic hash-based encoding for URL paths.

    We use a stable hash instead of LabelEncoder so the server
    doesn't need to have seen the exact paths during training.
    The model learns to split on numeric ranges, so consistency
    across train/serve is what matters.
    """
    return int(hashlib.md5(path.encode()).hexdigest()[:8], 16) % 1000


def encode_method(method: str) -> int:
    """Map HTTP methods to integers."""
    methods = {"GET": 0, "POST": 1, "PUT": 2, "DELETE": 3, "PATCH": 4}
    return methods.get(method.upper(), 5)


# ── Rule-Based Fallback ─────────────────────────────────────

def rule_based_predict(req: PredictRequest) -> int:
    """
    Deterministic fallback when no model is loaded.
    Mirrors the labeling logic used during training:
      - If query indicates heavy/matrix work → 1
      - If content_length is very large → 1
      - Otherwise → 0
    """
    if req.is_heavy_query == 1:
        return 1
    if req.content_length > 10000:
        return 1
    return 0


# ── Endpoints ────────────────────────────────────────────────

@app.post("/predict", response_model=PredictResponse)
async def predict(req: PredictRequest):
    """
    Predict whether an incoming request is light (0) or heavy (1).

    The Go gateway calls this endpoint with request metadata before
    routing. The response must be returned within ~5ms.
    """
    start = time.perf_counter()

    if using_fallback or model is None:
        label = rule_based_predict(req)
        elapsed_ms = (time.perf_counter() - start) * 1000

        if elapsed_ms > 5:
            logger.warning(f"⚠️  Slow prediction: {elapsed_ms:.2f}ms (fallback)")

        return PredictResponse(label=label, confidence=1.0, mode="fallback")

    # ── Model prediction ──
    features = np.array([[
        encode_path(req.url_path),
        encode_method(req.method),
        float(req.content_length),
        float(req.is_heavy_query),
    ]])

    label = int(model.predict(features)[0])

    # Get prediction probability for confidence
    proba = model.predict_proba(features)[0]
    confidence = float(max(proba))

    elapsed_ms = (time.perf_counter() - start) * 1000

    if elapsed_ms > 5:
        logger.warning(f"⚠️  Slow prediction: {elapsed_ms:.2f}ms")

    return PredictResponse(label=label, confidence=round(confidence, 4), mode="model")


@app.get("/health", response_model=HealthResponse)
async def health():
    """Health check endpoint."""
    return HealthResponse(
        status="ok",
        model_loaded=not using_fallback,
        mode="fallback" if using_fallback else "model",
    )


@app.post("/reload")
async def reload_model():
    """Hot-reload the model from disk without restarting the service."""
    success = load_model()
    return {
        "reloaded": success,
        "mode": "model" if not using_fallback else "fallback",
    }
