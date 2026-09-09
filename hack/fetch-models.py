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

import hashlib
import json
import os
import struct
import sys
import urllib.request
from pathlib import Path

HF = "https://huggingface.co"

POTION_REPO = "minishlab/potion-base-32M"
MINILM_REPO = "sentence-transformers/all-MiniLM-L6-v2"
POTION_REVISION = "1e5a03f8eeb2c98b928fbbd846f22f816360919f"
MINILM_REVISION = "1110a243fdf4706b3f48f1d95db1a4f5529b4d41"

POTION_MODEL_SHA256 = "99f6c33204c9231a7391871b7a3c91409b532c8f587a9ea44fc282303d8dec28"
POTION_TOKENIZER_SHA256 = "7d75cbc54318138807c401b0f0c9721117c628b39de8e8e0edb6cb17e0ee7d18"
MINILM_MODEL_SHA256 = "6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452"
MINILM_VOCAB_SHA256 = "07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3"

# Must match internal/embed/potion.go.
MAGIC = b"PULSEM2V"
VERSION = 1


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while chunk := handle.read(1 << 20):
            digest.update(chunk)
    return digest.hexdigest()


def download(url: str, target: Path, expected_sha256: str) -> None:
    if target.exists() and target.stat().st_size > 0:
        actual_sha256 = sha256(target)
        if actual_sha256 == expected_sha256:
            print(f"  have {target.name} ({actual_sha256})")
            return
        print(f"  replacing {target.name}: sha256 {actual_sha256} does not match")

    print(f"  fetching {url}")
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_suffix(target.suffix + ".partial")
    temporary.unlink(missing_ok=True)

    request = urllib.request.Request(url, headers={"User-Agent": "pulse-fetch-models"})
    digest = hashlib.sha256()
    with urllib.request.urlopen(request) as response, open(temporary, "wb") as handle:
        while chunk := response.read(1 << 20):
            handle.write(chunk)
            digest.update(chunk)

    actual_sha256 = digest.hexdigest()
    if actual_sha256 != expected_sha256:
        temporary.unlink()
        raise SystemExit(
            f"{url} has sha256 {actual_sha256}, expected {expected_sha256}"
        )

    temporary.replace(target)
    print(f"  verified sha256 {actual_sha256}")


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
    download(
        f"{HF}/{POTION_REPO}/resolve/{POTION_REVISION}/model.safetensors",
        safetensors,
        POTION_MODEL_SHA256,
    )
    download(
        f"{HF}/{POTION_REPO}/resolve/{POTION_REVISION}/tokenizer.json",
        tokenizer,
        POTION_TOKENIZER_SHA256,
    )

    dimensions = convert_potion(safetensors, root / "potion" / "model.bin")
    tokens = write_vocab_from_tokenizer(tokenizer, root / "potion" / "vocab.txt")
    print(f"  wrote potion/model.bin ({dimensions} dimensions) and "
          f"potion/vocab.txt ({tokens} tokens)")

    print("all-MiniLM-L6-v2 (cold path: correlation and novelty)")
    download(
        f"{HF}/{MINILM_REPO}/resolve/{MINILM_REVISION}/onnx/model.onnx",
        root / "minilm" / "model.onnx",
        MINILM_MODEL_SHA256,
    )
    download(
        f"{HF}/{MINILM_REPO}/resolve/{MINILM_REVISION}/vocab.txt",
        root / "minilm" / "vocab.txt",
        MINILM_VOCAB_SHA256,
    )
    print("  wrote minilm/model.onnx and minilm/vocab.txt")

    total = sum(f.stat().st_size for f in root.rglob("*") if f.is_file() and ".cache" not in f.parts)
    print(f"\nmodels ready in {root} ({total / (1 << 20):.0f} MiB baked into images)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
