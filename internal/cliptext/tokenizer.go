// Package cliptext encodes search queries with the pinned OpenAI CLIP text model.
package cliptext

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const Model = "ViT-B-32__openai"
const Revision = "a857c8de2c07bbcfa6646adfcf31b798845afa1e"
const ContextLength = 77
const Dimensions = 512

// This is CLIP's split pattern, with Unicode letters and individual digits.
var pieces = regexp.MustCompile(`<\|startoftext\|>|<\|endoftext\|>|'s|'t|'re|'ve|'m|'ll|'d|[\p{L}]+|[\p{N}]|[^\s\p{L}\p{N}]+`)

type Tokenizer struct {
	vocab map[string]int32
	ranks map[[2]string]int
	bytes [256]string
}

func LoadTokenizer(path string) (*Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Model struct {
			Type   string           `json:"type"`
			Vocab  map[string]int32 `json:"vocab"`
			Merges [][]string       `json:"merges"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	if file.Model.Type != "BPE" || len(file.Model.Vocab) != 49408 || file.Model.Vocab["<|startoftext|>"] != 49406 || file.Model.Vocab["<|endoftext|>"] != 49407 {
		return nil, fmt.Errorf("incompatible CLIP tokenizer")
	}
	t := &Tokenizer{vocab: file.Model.Vocab, ranks: map[[2]string]int{}}
	for i, pair := range file.Model.Merges {
		if len(pair) != 2 {
			return nil, fmt.Errorf("invalid BPE merge")
		}
		t.ranks[[2]string{pair[0], pair[1]}] = i
	}
	// GPT-2 byte-to-Unicode alphabet used by CLIP's ByteLevel pretokenizer.
	next := rune(256)
	for b := 0; b < 256; b++ {
		if b >= 33 && b <= 126 || b >= 161 && b <= 172 || b >= 174 {
			t.bytes[b] = string(rune(b))
		} else {
			t.bytes[b] = string(next)
			next++
		}
	}
	return t, nil
}

func (t *Tokenizer) Encode(query string) ([ContextLength]int32, error) {
	var result [ContextLength]int32
	// Immich clean_text collapses Unicode whitespace. NFC and Unicode lowercase
	// match the normalizer in this exact tokenizer.json (no HTML/ftfy rewriting).
	query = strings.Join(strings.FieldsFunc(query, unicode.IsSpace), " ")
	query = strings.Map(func(r rune) rune { return unicode.ToLower(r) }, strings.ReplaceAll(norm.NFC.String(query), "İ", "i\u0307"))
	tokens := []int32{49406}
	for _, piece := range pieces.FindAllString(query, -1) {
		if piece == "<|startoftext|>" {
			tokens = append(tokens, 49406)
			continue
		}
		if piece == "<|endoftext|>" {
			tokens = append(tokens, 49407)
			continue
		}
		word := make([]string, len([]byte(piece)))
		for i, b := range []byte(piece) {
			word[i] = t.bytes[b]
		}
		word[len(word)-1] += "</w>"
		for len(word) > 1 {
			best, position := int(^uint(0)>>1), -1
			for i := 0; i < len(word)-1; i++ {
				if rank, ok := t.ranks[[2]string{word[i], word[i+1]}]; ok && rank < best {
					best, position = rank, i
				}
			}
			if position < 0 {
				break
			}
			// Merge all occurrences of the chosen pair, preserving BPE tie order.
			left, right := word[position], word[position+1]
			merged := make([]string, 0, len(word))
			for i := 0; i < len(word); i++ {
				if i+1 < len(word) && word[i] == left && word[i+1] == right {
					merged = append(merged, left+right)
					i++
				} else {
					merged = append(merged, word[i])
				}
			}
			word = merged
		}
		for _, part := range word {
			id, ok := t.vocab[part]
			if !ok {
				return result, fmt.Errorf("CLIP token absent from vocabulary")
			}
			tokens = append(tokens, id)
		}
		if len(tokens) >= ContextLength-1 {
			break
		}
	}
	if len(tokens) > ContextLength-1 {
		tokens = tokens[:ContextLength-1]
	}
	tokens = append(tokens, 49407)
	copy(result[:], tokens)
	// Immich pads CLIP with its end-of-text token, not token zero.
	for i := len(tokens); i < ContextLength; i++ {
		result[i] = 49407
	}
	return result, nil
}
