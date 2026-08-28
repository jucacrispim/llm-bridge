#!/bin/bash
#
# fetch_kb.sh — download the knowledge base assets for the ONNX embedder.
#
# Fetches three files into ~/.cache/llm-bridge/ (override with LLM_BRIDGE_KB_DIR):
#   - model.onnx     from intfloat/multilingual-e5-small (HuggingFace)
#   - tokenizer.json from intfloat/multilingual-e5-small (HuggingFace)
#   - libonnxruntime.so (dlopened by the bridge at runtime)
#
# For the runtime lib we PREFER reusing one already installed on the system
# (much lighter — no extra ~150MB download), falling back to downloading it from
# the onnxruntime GitHub releases.
#
# After a download a checksum file (.sha256) is stored next to each asset; a
# re-run validates against it instead of re-downloading.
#
set -euo pipefail

CACHE_DIR="${LLM_BRIDGE_KB_DIR:-$HOME/.cache/llm-bridge}"
mkdir -p "$CACHE_DIR"

# --- model + tokenizer (HuggingFace) --------------------------------------
E5_REPO="intfloat/multilingual-e5-small"
HF_BASE="https://huggingface.co/${E5_REPO}/resolve/main/onnx"

# --- libonnxruntime --------------------------------------------------------
# Default release used when no system lib is found. Override the version with
# ONNXRT_VERSION.
ONNXRT_VERSION="${ONNXRT_VERSION:-1.21.0}"
ONNXRT_BASE="https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRT_VERSION}"

# Basic download helper: skips the file if it already exists and its checksum
# matches. Stores the sha256 next to the asset on first download.
fetch() { # $1=url  $2=dest
    local url="$1" dest="$2"
    local sum_file="${dest}.sha256"
    if [ -f "$dest" ] && [ -f "$sum_file" ]; then
        if [ "$(sha256sum "$dest" | awk '{print $1}')" = "$(cat "$sum_file")" ]; then
            echo "ok (cached)  $dest"
            return 0
        fi
        echo "checksum mismatch — re-downloading $dest"
    fi
    echo "downloading  $url"
    curl -fL --retry 3 -o "$dest.tmp" "$url"
    mv "$dest.tmp" "$dest"
    sha256sum "$dest" | awk '{print $1}' > "$sum_file"
    echo "ok           $dest"
}

# Prefer a system libonnxruntime if one is installed (glibc only; a musl build
# needs a musl .so so we skip the system lib there).
MUSL=0
if ldd --version 2>/dev/null | grep -qi musl; then
    MUSL=1
fi

lib_dest="$CACHE_DIR/libonnxruntime.so"
lib_installed=""
if [ "$MUSL" -eq 0 ]; then
    # Accept both the unversioned lib and the versioned form commonly installed
    # by distro packages (libonnxruntime.so.1.21, libonnxruntime.so.1.21.0, …).
    # `find -L` follows the symlink so we copy the real file, not a dangling link.
    for dir in /usr/lib/x86_64-linux-gnu /usr/local/lib /usr/lib; do
        found="$(find -L "$dir" -maxdepth 1 -name 'libonnxruntime.so*' 2>/dev/null | head -n1)"
        if [ -n "$found" ]; then
            lib_installed="$found"
            break
        fi
    done
fi

if [ -n "$lib_installed" ]; then
    cp -f "$lib_installed" "$lib_dest"
    echo "ok           $lib_dest (from $lib_installed)"
else
    # Download from GitHub releases. Artifact layout changed over releases, so
    # probe a couple of known names.
    local_arch="$(uname -m)"
    case "$local_arch" in
        x86_64) arch="x64" ;;
        aarch64|arm64) arch="aarch64" ;;
        *) echo "unsupported architecture: $local_arch" >&2; exit 1 ;;
    esac
    if [ "$MUSL" -eq 1 ]; then
        artifact="onnxruntime-linux-${arch}-musl-${ONNXRT_VERSION}.tgz"
    else
        artifact="onnxruntime-linux-${arch}-${ONNXRT_VERSION}.tgz"
    fi
    tmp="$(mktemp -d)"
    echo "downloading  ${ONNXRT_BASE}/${artifact}"
    curl -fL --retry 3 -o "$tmp/rt.tgz" "${ONNXRT_BASE}/${artifact}"
    tar xzf "$tmp/rt.tgz" -C "$tmp"
    # The .so lives in lib/ (sometimes lib64/).
    found="$(find "$tmp" -name 'libonnxruntime.so*' -type f -o -name 'libonnxruntime.so*' -type l | head -n1)"
    if [ -z "$found" ]; then
        echo "libonnxruntime.so not found in the release artifact" >&2
        rm -rf "$tmp"
        exit 1
    fi
    cp -fL "$found" "$lib_dest"
    rm -rf "$tmp"
    echo "ok           $lib_dest (from GitHub ${ONNXRT_VERSION})"
fi

# --- libtokenizers (prebuilt static lib) ------------------------------------
# The Go bindings (github.com/daulet/tokenizers) link against a static
# libtokenizers.a. Building it from source needs a full Rust toolchain, so we
# pull the prebuilt artifact. The version MUST match the tokenizers dependency
# in go.mod (daulet/tokenizers v1.27.0). Override with TOKENIZERS_VERSION.
TOKENIZERS_VERSION="${TOKENIZERS_VERSION:-v1.27.0}"
TK_ARCH="${TK_ARCH:-linux-amd64}"
TK_DIR="$CACHE_DIR/libtokenizers"
TK_URL="https://github.com/daulet/tokenizers/releases/download/${TOKENIZERS_VERSION}/libtokenizers.${TK_ARCH}.tar.gz"
mkdir -p "$TK_DIR"
if [ -f "$TK_DIR/libtokenizers.a" ]; then
    echo "ok (cached)  $TK_DIR/libtokenizers.a"
else
    echo "downloading  $TK_URL"
    tmp="$(mktemp -d)"
    curl -fL --retry 3 -o "$tmp/tk.tgz" "$TK_URL"
    tar xzf "$tmp/tk.tgz" -C "$TK_DIR"
    rm -rf "$tmp"
    echo "ok           $TK_DIR/libtokenizers.a"
fi

# --- model + tokenizer -----------------------------------------------------
fetch "${HF_BASE}/model.onnx"     "$CACHE_DIR/model.onnx"
fetch "${HF_BASE}/tokenizer.json" "$CACHE_DIR/tokenizer.json"

echo ""
echo "All knowledge base assets are in $CACHE_DIR:"
ls -lh "$CACHE_DIR"/model.onnx "$CACHE_DIR"/tokenizer.json "$CACHE_DIR"/libonnxruntime.so "$CACHE_DIR"/libtokenizers/libtokenizers.a

echo ""
echo "Next steps:"
echo "  make build-kb          # build the bridge with the ONNX embedder (links libtokenizers)"
echo "  make run ARGS=\"-provider ...\"  # then the KB is seeded on first use per project"
