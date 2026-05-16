"""
──────────────────────────────────────────────────────────────
 NeuroRoute — ML Model Training Pipeline

 Loads traffic.csv from the gateway's logs, engineers features,
 labels each request as light (0) or heavy (1), and trains a
 RandomForestClassifier. The trained model is exported to
 model.pkl for the prediction server.

 Usage:
   python train.py [--data PATH] [--output PATH] [--threshold MS]
──────────────────────────────────────────────────────────────
"""

import argparse
import os
import sys

import joblib
import numpy as np
import pandas as pd
from sklearn.ensemble import RandomForestClassifier
from sklearn.metrics import (
    accuracy_score,
    classification_report,
    confusion_matrix,
)
from sklearn.model_selection import train_test_split
from sklearn.preprocessing import LabelEncoder


def load_and_prepare(data_path: str, threshold_ms: float = 200.0) -> pd.DataFrame:
    """Load traffic.csv and prepare features + labels."""
    print(f"📂 Loading data from: {data_path}")

    df = pd.read_csv(data_path)
    print(f"   Loaded {len(df)} rows")

    # ── Label: 0 = light (< threshold), 1 = heavy (>= threshold) ──
    df["label"] = (df["processing_time_ms"] >= threshold_ms).astype(int)

    light_count = (df["label"] == 0).sum()
    heavy_count = (df["label"] == 1).sum()
    print(f"   Labels: {light_count} light / {heavy_count} heavy")
    print(f"   Threshold: {threshold_ms}ms")

    return df


def engineer_features(df: pd.DataFrame) -> tuple[np.ndarray, np.ndarray]:
    """
    Extract the 4 features required by the gateway:
      1. url_path_encoded  — LabelEncoded URL path
      2. method_encoded    — LabelEncoded HTTP method
      3. content_length    — Raw content length (bytes)
      4. is_heavy_query    — 1 if query_params contains 'heavy' or 'matrix'
    """
    # Encode categorical features
    path_encoder = LabelEncoder()
    method_encoder = LabelEncoder()

    df["url_path_encoded"] = path_encoder.fit_transform(df["url_path"].fillna(""))
    df["method_encoded"] = method_encoder.fit_transform(df["http_method"].fillna(""))

    # Content length (fill missing with 0)
    df["content_length"] = df["content_length"].fillna(0).astype(float)

    # Is heavy query — check if query params indicate heavy work
    df["is_heavy_query"] = df["query_params"].fillna("").apply(
        lambda q: 1 if ("heavy" in str(q).lower() or "matrix" in str(q).lower()) else 0
    )

    feature_cols = ["url_path_encoded", "method_encoded", "content_length", "is_heavy_query"]
    X = df[feature_cols].values
    y = df["label"].values

    print(f"   Features shape: {X.shape}")
    print(f"   Feature columns: {feature_cols}")

    # Save encoders for reference
    return X, y


def train_model(
    X: np.ndarray,
    y: np.ndarray,
    n_estimators: int = 50,
    max_depth: int = 8,
    test_size: float = 0.2,
) -> RandomForestClassifier:
    """Train a RandomForestClassifier and report metrics."""
    print(f"\n🧠 Training RandomForestClassifier")
    print(f"   n_estimators={n_estimators}, max_depth={max_depth}")

    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=test_size, random_state=42, stratify=y
    )
    print(f"   Train: {len(X_train)} | Test: {len(X_test)}")

    model = RandomForestClassifier(
        n_estimators=n_estimators,
        max_depth=max_depth,
        random_state=42,
        n_jobs=-1,
    )
    model.fit(X_train, y_train)

    # ── Evaluation ──
    y_pred = model.predict(X_test)
    accuracy = accuracy_score(y_test, y_pred)

    print(f"\n📊 Results:")
    print(f"   Accuracy: {accuracy:.4f}")
    print(f"\n   Classification Report:")
    print(classification_report(y_test, y_pred, target_names=["light", "heavy"]))

    cm = confusion_matrix(y_test, y_pred)
    print(f"   Confusion Matrix:")
    print(f"   {cm}")

    # Feature importance
    feature_names = ["url_path", "method", "content_length", "is_heavy_query"]
    importances = model.feature_importances_
    print(f"\n   Feature Importance:")
    for name, imp in sorted(zip(feature_names, importances), key=lambda x: -x[1]):
        bar = "█" * int(imp * 40)
        print(f"     {name:20s} {imp:.4f} {bar}")

    return model


def main():
    parser = argparse.ArgumentParser(description="NeuroRoute ML Training Pipeline")
    parser.add_argument(
        "--data",
        default="data/traffic.csv",
        help="Path to traffic.csv (default: data/traffic.csv)",
    )
    parser.add_argument(
        "--output",
        default="model.pkl",
        help="Path to save trained model (default: model.pkl)",
    )
    parser.add_argument(
        "--threshold",
        type=float,
        default=200.0,
        help="Processing time threshold in ms for heavy label (default: 200)",
    )
    parser.add_argument(
        "--estimators",
        type=int,
        default=50,
        help="Number of trees in Random Forest (default: 50)",
    )
    parser.add_argument(
        "--max-depth",
        type=int,
        default=8,
        help="Max tree depth (default: 8)",
    )
    args = parser.parse_args()

    # ── Validate input ──
    if not os.path.exists(args.data):
        print(f"❌ Data file not found: {args.data}")
        print("   Run `make harvest` to collect traffic data first.")
        sys.exit(1)

    # ── Pipeline ──
    df = load_and_prepare(args.data, args.threshold)
    X, y = engineer_features(df)
    model = train_model(X, y, n_estimators=args.estimators, max_depth=args.max_depth)

    # ── Export ──
    joblib.dump(model, args.output)
    print(f"\n✅ Model saved to: {args.output}")
    print(f"   File size: {os.path.getsize(args.output) / 1024:.1f} KB")


if __name__ == "__main__":
    main()
