# Prepare the model artifacts

Pulse uses two local embedding models for different workloads. Potion runs in the probe runner on passing HTTP bodies; MiniLM runs in the optional incident engine on failures. This chapter downloads reproducible inputs, converts Potion into Pulse's runtime format, and verifies the hot-path model. It does not install the incident engine yet.

You need Python 3, `curl`, about 350 MiB of free disk space, and the Pulse source revision selected in the previous chapter. Start at the repository root and record that revision:

```sh
git rev-parse HEAD
```

## Identify the immutable inputs

Pulse pins each Hugging Face repository to a commit and verifies every downloaded file with SHA-256. Inspect the constants before trusting the helper:

```sh
grep -E '(_REPO|_REVISION|_SHA256) =' hack/fetch-models.py
```

The pinned inputs for this book revision are:

| Use | Upstream file | Revision | License |
| --- | --- | --- | --- |
| Passing-body drift | `minishlab/potion-base-32M` `model.safetensors` and `tokenizer.json` | `1e5a03f8eeb2c98b928fbbd846f22f816360919f` | [MIT](https://huggingface.co/minishlab/potion-base-32M) |
| Failure similarity and novelty | `sentence-transformers/all-MiniLM-L6-v2` `onnx/model.onnx` and `vocab.txt` | `1110a243fdf4706b3f48f1d95db1a4f5529b4d41` | [Apache-2.0](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2) |

The repository commits and hashes are part of the artifact identity. A model name alone is not reproducible because an upstream `main` branch can change.

## Download the four source files

Create the same paths used by the image builds:

```sh
mkdir -p hack/models/.cache/potion hack/models/minilm
```

Download Potion's embedding matrix and tokenizer:

```sh
curl --fail --location \
  --output hack/models/.cache/potion/model.safetensors \
  https://huggingface.co/minishlab/potion-base-32M/resolve/1e5a03f8eeb2c98b928fbbd846f22f816360919f/model.safetensors
curl --fail --location \
  --output hack/models/.cache/potion/tokenizer.json \
  https://huggingface.co/minishlab/potion-base-32M/resolve/1e5a03f8eeb2c98b928fbbd846f22f816360919f/tokenizer.json
```

Download the upstream ONNX export of MiniLM and its vocabulary:

```sh
curl --fail --location \
  --output hack/models/minilm/model.onnx \
  https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/1110a243fdf4706b3f48f1d95db1a4f5529b4d41/onnx/model.onnx
curl --fail --location \
  --output hack/models/minilm/vocab.txt \
  https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/1110a243fdf4706b3f48f1d95db1a4f5529b4d41/vocab.txt
```

Verify bytes before conversion or image construction:

```sh
cat <<'EOF' | sha256sum --check
99f6c33204c9231a7391871b7a3c91409b532c8f587a9ea44fc282303d8dec28  hack/models/.cache/potion/model.safetensors
7d75cbc54318138807c401b0f0c9721117c628b39de8e8e0edb6cb17e0ee7d18  hack/models/.cache/potion/tokenizer.json
6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452  hack/models/minilm/model.onnx
07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3  hack/models/minilm/vocab.txt
EOF
```

Expect four `OK` lines. Do not continue after a mismatch. Delete only the mismatched file, check available disk space and any proxy behavior, then download it again.

## Convert Potion for the runner

Read `hack/fetch-models.py` before executing it. The utility rechecks the four hashes, then performs the conversion that would be error-prone to reproduce with shell tools:

```sh
python3 hack/fetch-models.py hack/models
```

For Potion, it reads the largest two-dimensional tensor from safetensors, converts values to little-endian float32 when necessary, and writes `hack/models/potion/model.bin`. Its 20-byte header contains the eight-byte magic `PULSEM2V`, format version, vector dimension, and vocabulary row count. The remaining bytes are the row-major embedding matrix.

The utility also turns the tokenizer's WordPiece map into `hack/models/potion/vocab.txt`. A token's line number is its numeric ID, which lets the Go runtime load the vocabulary with a single scan. The runner tokenizes each body, looks up static token vectors, averages them, and L2-normalizes the resulting 512-dimensional vector. It does not need safetensors, Python, or cgo at runtime.

MiniLM is different: Hugging Face already supplies the ONNX graph, so Pulse copies it without conversion. The incident engine uses WordPiece special tokens, transformer inference, mean pooling, and L2 normalization to produce a 384-dimensional vector. Potion and MiniLM vectors belong to different spaces and must never be compared.

Inspect the generated header and artifact sizes:

```sh
python3 - <<'PY'
import struct
from pathlib import Path

path = Path("hack/models/potion/model.bin")
with path.open("rb") as model:
    magic = model.read(8)
    version, dimensions, rows = struct.unpack("<III", model.read(12))
print(f"magic={magic.decode()} version={version} dimensions={dimensions} rows={rows}")
PY
du -h hack/models/potion/model.bin hack/models/potion/vocab.txt \
  hack/models/minilm/model.onnx hack/models/minilm/vocab.txt
```

Expect `magic=PULSEM2V`, version `1`, and `dimensions=512`. File sizes are rounded by `du`; the helper reports about 210 MiB of converted artifacts with the pinned inputs. The ignored `.cache` directory retains the raw Potion inputs and is excluded from container build contexts.

## Prove the hot-path model is real

Run only the tests that load the converted Potion files:

```sh
go test ./internal/embed -run 'TestRealPotion' -count=1 -v
```

The output must show the real tests running and passing. A `SKIP` result means the files were not found and is not model validation. These tests establish a 512-dimensional Potion space, zero distance for identical bodies, semantic ordering, and separation of a maintenance page from a normal response at the default drift threshold.

Do not claim MiniLM runtime validation yet. Its Go backend is selected with the `onnx` build tag and requires a compatible `libonnxruntime.so`; the incident-engine image chapter will build that native combination and run the tagged tests. Building without the tag deliberately returns an unavailable-backend error.

Keep the artifacts for the image chapter. They are ignored by Git, but they consume local disk. To reset only model state:

```sh
rm -rf hack/models/.cache hack/models/potion/model.bin \
  hack/models/potion/vocab.txt hack/models/minilm/model.onnx \
  hack/models/minilm/vocab.txt
```

Checkpoint: explain why the model repository name is not a complete artifact identity, why Potion is converted while MiniLM is not, and why a skipped real-model test cannot prove model loading.
