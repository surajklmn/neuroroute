"""
──────────────────────────────────────────────────────────────
 NeuroRoute — ML Model Training Pipeline (Embedded Go Exporter)

 Loads traffic.csv from the gateway's logs, engineers features,
 labels each request into 3 classes (light, medium, heavy) based
 on the raw worker execution time, and trains a RandomForestClassifier.
 It then exports the trained classifier into an inline, highly
 optimized Go predictor (nested if-else rules).
──────────────────────────────────────────────────────────────
"""

import argparse
import os
import sys
import hashlib
import numpy as np
import pandas as pd
from sklearn.ensemble import RandomForestClassifier
from sklearn.metrics import (
    accuracy_score,
    classification_report,
    confusion_matrix,
)
from sklearn.model_selection import train_test_split


def encode_path(path: str) -> int:
    """Deterministic path encoder: MD5 hex digest, first 8 hex characters, % 1000."""
    h = hashlib.md5(str(path).encode('utf-8')).hexdigest()[:8]
    return int(h, 16) % 1000


def encode_method(method: str) -> int:
    """Deterministic method encoder."""
    m = str(method).upper().strip()
    mapping = {'GET': 0, 'POST': 1, 'PUT': 2, 'DELETE': 3, 'PATCH': 4}
    return mapping.get(m, 5)


def load_and_prepare(data_path: str) -> pd.DataFrame:
    """Load traffic.csv and prepare labels."""
    print(f"📂 Loading data from: {data_path}")

    df = pd.read_csv(data_path, on_bad_lines='skip')
    print(f"   Loaded {len(df)} rows")

    # ── Map labels to 3 classes ──
    # Class 0: < 10 ms
    # Class 1: 10 ms - 200 ms
    # Class 2: > 200 ms
    df["label"] = df["processing_time_ms"].apply(
        lambda ms: 0 if ms < 10.0 else (1 if ms <= 200.0 else 2)
    )

    class0 = (df["label"] == 0).sum()
    class1 = (df["label"] == 1).sum()
    class2 = (df["label"] == 2).sum()
    print(f"   Labels: {class0} light (C0) / {class1} medium (C1) / {class2} heavy (C2)")

    return df


def engineer_features(df: pd.DataFrame) -> tuple[np.ndarray, np.ndarray]:
    """
    Extract the 6 aligned features:
      1. url_path_encoded  — Deterministic hash of URL path
      2. method_encoded    — Deterministic http method encoding
      3. content_length    — Raw content length (bytes)
      4. is_heavy_query    — 1 if query_params contains 'heavy' or 'matrix'
      5. json_key_count    — Count of JSON keys
      6. keyword_frequency — Frequency of heavy keywords
    """
    df["url_path_encoded"] = df["url_path"].fillna("").apply(encode_path)
    df["method_encoded"] = df["http_method"].fillna("").apply(encode_method)

    df["content_length"] = df["content_length"].fillna(0).astype(float)

    df["is_heavy_query"] = df["query_params"].fillna("").apply(
        lambda q: 1.0 if ("heavy" in str(q).lower() or "matrix" in str(q).lower()) else 0.0
    )

    # Align with new structural body profiling features
    if "json_key_count" not in df.columns:
        df["json_key_count"] = 0.0
    else:
        df["json_key_count"] = df["json_key_count"].fillna(0.0).astype(float)

    if "keyword_frequency" not in df.columns:
        df["keyword_frequency"] = 0.0
    else:
        df["keyword_frequency"] = df["keyword_frequency"].fillna(0.0).astype(float)

    feature_cols = [
        "url_path_encoded", "method_encoded", "content_length",
        "is_heavy_query", "json_key_count", "keyword_frequency"
    ]
    X = df[feature_cols].values
    y = df["label"].values

    print(f"   Features shape: {X.shape}")
    print(f"   Feature columns: {feature_cols}")

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

    # Check if we have multiple classes represented in the training set
    unique_classes = np.unique(y)
    stratify_y = y if len(unique_classes) > 1 else None

    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=test_size, random_state=42, stratify=stratify_y
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
    
    target_names = ["light", "medium", "heavy"]
    actual_target_names = [target_names[int(c)] for c in unique_classes]
    print(classification_report(y_test, y_pred, labels=unique_classes, target_names=actual_target_names))

    # Feature importance
    feature_names = [
        "url_path_encoded", "method_encoded", "content_length",
        "is_heavy_query", "json_key_count", "keyword_frequency"
    ]
    importances = model.feature_importances_
    print(f"\n   Feature Importance:")
    for name, imp in sorted(zip(feature_names, importances), key=lambda x: -x[1]):
        bar = "█" * int(imp * 40)
        print(f"     {name:20s} {imp:.4f} {bar}")

    return model


def export_to_go(model: RandomForestClassifier, output_path: str):
    """Export the trained scikit-learn RandomForestClassifier to inline nested Go if-else branches."""
    print(f"\n🚀 Generating Go Predictor → {output_path}")
    
    n_classes = len(model.classes_)
    lines = []
    lines.append("// Code generated by ml/train.py. DO NOT EDIT.")
    lines.append("package main")
    lines.append("")
    lines.append("// Predict returns the predicted class (0, 1, or 2) using the embedded Random Forest model.")
    lines.append("func Predict(features []float64) int {")
    lines.append("\t// Accumulators for probabilities across all estimators")
    lines.append("\tvar p0, p1, p2 float64")
    lines.append("")

    for tree_idx, estimator in enumerate(model.estimators_):
        lines.append(f"\t// Tree {tree_idx}")
        lines.append("\t{")
        lines.append("\t\tvar t0, t1, t2 float64")
        
        tree = estimator.tree_

        def recurse(node_id: int, depth: int):
            indent = "\t" * depth
            left = tree.children_left[node_id]
            right = tree.children_right[node_id]

            if left == -1 and right == -1:
                # Leaf node: get raw sample counts and compute normalized probabilities
                val = tree.value[node_id][0]
                total = sum(val)
                probs = [0.0] * n_classes
                if total > 0:
                    probs = [v / total for v in val]

                # Map model's classes to aligned target classes (0, 1, 2)
                aligned = [0.0, 0.0, 0.0]
                for idx, c in enumerate(model.classes_):
                    if c in [0, 1, 2]:
                        aligned[int(c)] = probs[idx]

                lines.append(f"{indent}t0 = {aligned[0]:.6f}")
                lines.append(f"{indent}t1 = {aligned[1]:.6f}")
                lines.append(f"{indent}t2 = {aligned[2]:.6f}")
                return

            feature = tree.feature[node_id]
            threshold = tree.threshold[node_id]

            lines.append(f"{indent}if features[{feature}] <= {threshold:.6f} {{")
            recurse(left, depth + 1)
            lines.append(f"{indent}}} else {{")
            recurse(right, depth + 1)
            lines.append(f"{indent}}}")

        recurse(0, 2)
        lines.append("\t\tp0 += t0")
        lines.append("\t\tp1 += t1")
        lines.append("\t\tp2 += t2")
        lines.append("\t}")
        lines.append("")

    lines.append("\t// Argmax class selection")
    lines.append("\tif p0 >= p1 && p0 >= p2 {")
    lines.append("\t\treturn 0")
    lines.append("\t} else if p1 >= p0 && p1 >= p2 {")
    lines.append("\t\treturn 1")
    lines.append("\t} else {")
    lines.append("\t\treturn 2")
    lines.append("\t}")
    lines.append("}")

    # Ensure parent directory exists before writing
    os.makedirs(os.path.dirname(os.path.abspath(output_path)), exist_ok=True)
    with open(output_path, "w") as f:
        f.write("\n".join(lines) + "\n")

    print(f"✅ Embedded Go Predictor successfully generated! Total estimators: {len(model.estimators_)}")


def main():
    parser = argparse.ArgumentParser(description="NeuroRoute ML Training Pipeline")
    parser.add_argument(
        "--data",
        default="data/traffic.csv",
        help="Path to traffic.csv (default: data/traffic.csv)",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="Apply Label Correction to filter out queueing congestion noise",
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

    if not os.path.exists(args.data):
        print(f"❌ Data file not found: {args.data}")
        print("   Run `make harvest` to collect traffic data first.")
        sys.exit(1)

    df = load_and_prepare(args.data)

    if args.clean:
        print("🧹 Applying Semi-Supervised Label Correction (3-class Mapping)...")
        # Direct rule: if heavy or matrix query parameters are present -> Class 2 (Heavy)
        # Otherwise -> Class 0 (Light)
        queries = df["query_params"].fillna("").astype(str).str.lower()
        df["label"] = queries.apply(lambda q: 2 if ("heavy" in q or "matrix" in q) else 0)

    X, y = engineer_features(df)
    model = train_model(X, y, n_estimators=args.estimators, max_depth=args.max_depth)

    # Dynamic path discovery for Go predictor output
    script_dir = os.path.dirname(os.path.abspath(__file__))
    go_predictor_path = os.path.abspath(os.path.join(script_dir, "../gateway/predictor.go"))
    
    export_to_go(model, go_predictor_path)


if __name__ == "__main__":
    main()
