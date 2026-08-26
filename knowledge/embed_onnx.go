//go:build knowledge_onnx

package knowledge

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// onnxEmbedder is the real ONNX-backed embedder, only compiled in with the
// knowledge_onnx build tag. It loads a multilingual-e5-small style model
// (tokenizer.json + model.onnx) and runs the standard sentence-transformers
// pipeline: tokenize -> forward -> mean pooling -> L2 normalize.
//
// The E5 `query: `/`passage: ` prefixes are applied by the Manager/callers
// (see Embedder interface), so Embed just tokenizes the text it is given.
type onnxEmbedder struct {
	tok      *tokenizers.Tokenizer
	session  *ort.DynamicAdvancedSession
	inNames  []string
	outNames []string
	hidden   int // last dimension of the last output tensor
}

// NewEmbedder loads the ONNX model and tokenizer and returns a real Embedder.
// modelPath is the .onnx file and tokenizerPath the tokenizer.json. The
// libonnxruntime shared library path is taken from LLM_BRIDGE_ONNXRUNTIME_LIB
// (default "onnxruntime.so"). Any failure here (missing lib, bad model, …)
// returns an error so callers can fall back to a disabled embedder.
func NewEmbedder(modelPath, tokenizerPath string) (Embedder, error) {
	if modelPath == "" || tokenizerPath == "" {
		return nil, errors.New("knowledge: onnx model and tokenizer paths required")
	}

	lib := os.Getenv("LLM_BRIDGE_ONNXRUNTIME_LIB")
	if lib == "" {
		lib = "onnxruntime.so"
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("knowledge: initialize onnxruntime: %w", err)
	}

	tok, err := tokenizers.FromFile(tokenizerPath)
	if err != nil {
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: load tokenizer: %w", err)
	}

	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		tok.Close()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: inspect model: %w", err)
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		tok.Close()
		ort.DestroyEnvironment()
		return nil, errors.New("knowledge: model has no inputs/outputs")
	}
	inNames := make([]string, len(inputs))
	outNames := make([]string, len(outputs))
	for i, in := range inputs {
		inNames[i] = in.Name
	}
	for i, out := range outputs {
		outNames[i] = out.Name
	}

	opts, err := ort.NewSessionOptions()
	if err != nil {
		tok.Close()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: session options: %w", err)
	}
	defer opts.Destroy()
	sess, err := ort.NewDynamicAdvancedSession(modelPath, inNames, outNames, opts)
	if err != nil {
		tok.Close()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: load onnx session: %w", err)
	}

	// The last output is the hidden states tensor shaped [1, seq, hidden];
	// take its last dimension as the embedding size.
	last := outputs[len(outputs)-1]
	hidden := 0
	if d := last.Dimensions; len(d) > 0 {
		hidden = int(d[len(d)-1])
	}

	return &onnxEmbedder{
		tok:      tok,
		session:  sess,
		inNames:  inNames,
		outNames: outNames,
		hidden:   hidden,
	}, nil
}

// Embed runs the text through the model and returns the mean-pooled,
// L2-normalized sentence vector. text is expected to already carry the E5
// prefix applied by the caller.
func (o *onnxEmbedder) Embed(text string, isQuery bool) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("knowledge: empty input")
	}

	enc, err := o.tok.EncodeWithOptionsErr(text, true, tokenizers.WithReturnAttentionMask())
	if err != nil {
		return nil, fmt.Errorf("knowledge: tokenize: %w", err)
	}
	ids := enc.IDs
	seq := len(ids)
	if seq == 0 {
		return nil, errors.New("knowledge: empty tokenization")
	}

	// Build int64 input tensors. input_ids/attention_mask use the token ids /
	// mask; any extra int inputs (e.g. token_type_ids) get zeros.
	seqInt := int64(seq)
	idData := make([]int64, seq)
	maskData := make([]int64, seq)
	for i, id := range ids {
		idData[i] = int64(id)
		maskData[i] = 1
	}
	zeroData := make([]int64, seq)

	inputs := make([]ort.Value, 0, len(o.inNames))
	inputTensors := make([]*ort.Tensor[int64], 0, len(o.inNames))
	for _, name := range o.inNames {
		var data []int64
		switch {
		case strings.Contains(name, "attention"):
			data = maskData
		case strings.Contains(name, "token_type"):
			data = zeroData
		default:
			data = idData
		}
		t, err := ort.NewTensor(ort.NewShape(1, seqInt), data)
		if err != nil {
			o.destroyInputs(inputTensors)
			return nil, fmt.Errorf("knowledge: input tensor %s: %w", name, err)
		}
		inputTensors = append(inputTensors, t)
		inputs = append(inputs, t)
	}
	defer o.destroyInputs(inputTensors)

	outShape := ort.NewShape(1, seqInt, int64(o.hidden))
	outTensor, err := ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return nil, fmt.Errorf("knowledge: output tensor: %w", err)
	}
	defer outTensor.Destroy()
	outputs := []ort.Value{outTensor}

	if err := o.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("knowledge: run model: %w", err)
	}
	return meanPool(outTensor.GetData(), seq, o.hidden), nil
}

// destroyInputs releases any input tensors created so far (defers free cleanly).
func (o *onnxEmbedder) destroyInputs(ts []*ort.Tensor[int64]) {
	for _, t := range ts {
		if t != nil {
			t.Destroy()
		}
	}
}

// meanPool averages the token hidden states over the masked positions and
// L2-normalizes the result. Returns a fresh vector of size hiddenDim.
func meanPool(hidden []float32, seqLen, hiddenDim int) []float32 {
	out := make([]float32, hiddenDim)
	for s := 0; s < seqLen; s++ {
		base := s * hiddenDim
		for h := 0; h < hiddenDim; h++ {
			out[h] += hidden[base+h]
		}
	}
	count := float32(seqLen)
	if count > 0 {
		for h := range out {
			out[h] /= count
		}
	}
	// L2 normalize.
	var norm float64
	for _, v := range out {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		inv := float32(1 / norm)
		for i := range out {
			out[i] *= inv
		}
	}
	return out
}
