#!/usr/bin/env python3
"""Download and convert the models Pulse bakes into its images.

Two models, for two very different jobs:

  potion-base-32M  the hot path. Static Model2Vec embeddings, scored on every
                   passing check for body drift. Converted here from
                   safetensors into a flat binary the Go loader can read with a
                   header parse and one slice — no safetensors parser, no dtype
                   matrix, and no cgo in the probe runner.

  all-MiniLM-L6-v2 the cold path. A real transformer, run only on failures by
                   the incident engine, where semantic precision matters and
                   volume is three orders of magnitude lower.

Only the Python standard library is required for the F32 path. numpy is used
only if a model ships F16 weights.
"""

import json
import os
import struct
import sys
import urllib.request
from pathlib import Path

HF = "https://huggingface.co"

POTION_REPO = "minishlab/potion-base-32M"
MINILM_REPO = "sentence-transformers/all-MiniLM-L6-v2"

# Must match internal/embed/potion.go.
MAGIC = b"PULSEM2V"
VERSION = 1


def download(url: str, target: Path) -> None:
    if target.exists() and target.stat().st_size > 0:
        print(f"  have {target.name}")
        return

    print(f"  fetching {url}")
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_suffix(target.suffix + ".partial")

    request = urllib.request.Request(url, headers={"User-Agent": "pulse-fetch-models"})
    with urllib.request.urlopen(request) as response, open(temporary, "wb") as handle:
        while chunk := response.read(1 << 20):
            handle.write(chunk)

    temporary.rename(target)


def read_safetensors(path: Path):
    """Return (name, dtype, shape, raw_bytes) for the largest 2-D tensor.

    Model2Vec files hold a single embedding matrix, but the tensor name has
    varied across releases, so the matrix is identified by shape rather than by
    a hardcoded key.
    """
    with open(path, "rb") as handle:
        header_length = struct.unpack("<Q", handle.read(8))[0]
        header = json.loads(handle.read(header_length))
        body = handle.read()

    best = None
    for name, meta in header.items():
        if name == "__metadata__":
            continue
        shape = meta.get("shape", [])
        if len(shape) != 2:
            continue
        if best is None or shape[0] * shape[1] > best[2][0] * best[2][1]:
            best = (name, meta["dtype"], shape, meta["data_offsets"])

    if best is None:
        raise SystemExit(f"{path} contains no 2-D tensor")

    name, dtype, shape, (start, end) = best
    return name, dtype, shape, body[start:end]


def to_float32(dtype: str, raw: bytes, count: int):
    if dtype == "F32":
        return struct.unpack(f"<{count}f", raw)

    if dtype == "F16":
        try:
            import numpy
        except ImportError:
            raise SystemExit(
                "this model ships F16 weights; install numpy to convert it "
                "(pip install numpy)"
            )
        return numpy.frombuffer(raw, dtype=numpy.float16).astype(numpy.float32).tolist()

    raise SystemExit(f"unsupported tensor dtype {dtype}")


def convert_potion(source: Path, destination: Path) -> int:
    name, dtype, shape, raw = read_safetensors(source)
    rows, dimensions = shape
    print(f"  matrix {name}: {rows} x {dimensions} ({dtype})")

    values = to_float32(dtype, raw, rows * dimensions)

    destination.parent.mkdir(parents=True, exist_ok=True)
    with open(destination, "wb") as handle:
        handle.write(MAGIC)
        handle.write(struct.pack("<III", VERSION, dimensions, rows))
        handle.write(struct.pack(f"<{len(values)}f", *values))

    return dimensions


def write_vocab_from_tokenizer(tokenizer_path: Path, destination: Path) -> int:
    """Flatten a HuggingFace tokenizer.json into a line-indexed vocab.txt.

    The Go tokenizer reads vocab.txt, where the line number IS the token ID.
    Emitting that here keeps the runtime loader to a single scan.
    """
    with open(tokenizer_path, encoding="utf-8") as handle:
        tokenizer = json.load(handle)

    vocab = tokenizer.get("model", {}).get("vocab")
    if not isinstance(vocab, dict):
        raise SystemExit(f"{tokenizer_path} has no WordPiece vocabulary")

    ordered = [""] * (max(vocab.values()) + 1)
    for token, index in vocab.items():
        ordered[index] = token

    destination.parent.mkdir(parents=True, exist_ok=True)
    with open(destination, "w", encoding="utf-8") as handle:
        handle.write("\n".join(ordered) + "\n")

    return len(ordered)


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else "hack/models")
    cache = root / ".cache"

    print("potion-base-32M (hot path: body drift)")
    safetensors = cache / "potion" / "model.safetensors"
    tokenizer = cache / "potion" / "tokenizer.json"
    download(f"{HF}/{POTION_REPO}/resolve/main/model.safetensors", safetensors)
    download(f"{HF}/{POTION_REPO}/resolve/main/tokenizer.json", tokenizer)

    dimensions = convert_potion(safetensors, root / "potion" / "model.bin")
    tokens = write_vocab_from_tokenizer(tokenizer, root / "potion" / "vocab.txt")
    print(f"  wrote potion/model.bin ({dimensions} dimensions) and "
          f"potion/vocab.txt ({tokens} tokens)")

    print("all-MiniLM-L6-v2 (cold path: correlation and novelty)")
    download(f"{HF}/{MINILM_REPO}/resolve/main/onnx/model.onnx", root / "minilm" / "model.onnx")
    download(f"{HF}/{MINILM_REPO}/resolve/main/vocab.txt", root / "minilm" / "vocab.txt")
    print("  wrote minilm/model.onnx and minilm/vocab.txt")

    total = sum(f.stat().st_size for f in root.rglob("*") if f.is_file() and ".cache" not in f.parts)
    print(f"\nmodels ready in {root} ({total / (1 << 20):.0f} MiB baked into images)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
