package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVocab builds a vocab.txt whose line numbers are the token IDs.
func writeVocab(t *testing.T, tokens []string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vocab.txt")
	if err := os.WriteFile(path, []byte(strings.Join(tokens, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// A miniature bert-base-uncased-style vocabulary, ordered so IDs are explicit.
func testVocabTokens() []string {
	return []string{
		"[PAD]",       // 0
		"[UNK]",       // 1
		"[CLS]",       // 2
		"[SEP]",       // 3
		"connection",  // 4
		"refused",     // 5
		"time",        // 6
		"##out",       // 7
		"un",          // 8
		"##available", // 9
		":",           // 10
		"cafe",        // 11
		"upstream",    // 12
	}
}

func testTokenizer(t *testing.T) *WordPiece {
	t.Helper()

	tokenizer, err := LoadWordPiece(writeVocab(t, testVocabTokens()), true)
	if err != nil {
		t.Fatalf("LoadWordPiece() error = %v", err)
	}
	return tokenizer
}

func TestWordPieceGreedyLongestMatch(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)

	// "timeout" splits into "time" + "##out"; "unavailable" into "un" +
	// "##available". This greedy longest-match-first behavior is what makes
	// the same failure text tokenize identically every time.
	got := tokenizer.Encode("timeout unavailable", EncodeOptions{})
	want := []int32{6, 7, 8, 9}

	if len(got) != len(want) {
		t.Fatalf("Encode() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Encode() = %v, want %v", got, want)
		}
	}
}

func TestWordPieceLowercasesAndStripsAccents(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)

	plain := tokenizer.Encode("cafe", EncodeOptions{})
	accented := tokenizer.Encode("CAFÉ", EncodeOptions{})

	if len(plain) != 1 || len(accented) != 1 || plain[0] != accented[0] {
		t.Fatalf("accented form tokenized as %v, plain as %v", accented, plain)
	}
	if plain[0] != 11 {
		t.Fatalf("Encode(\"cafe\") = %v, want token ID 11", plain)
	}
}

func TestWordPieceSplitsPunctuationIntoOwnTokens(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)

	// Punctuation becomes its own token rather than gluing to a word, so
	// "refused:" and "refused" share the "refused" token.
	got := tokenizer.Encode("refused:", EncodeOptions{})
	want := []int32{5, 10}

	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Encode(\"refused:\") = %v, want %v", got, want)
	}
}

func TestWordPieceUnknownWordBecomesUNK(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)

	got := tokenizer.Encode("zzzz", EncodeOptions{})
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("Encode(\"zzzz\") = %v, want the [UNK] ID 1", got)
	}
}

// Transformers need [CLS]/[SEP]; static embeddings must not get them, or every
// mean is dragged toward the same two constant vectors.
func TestWordPieceSpecialTokensAreOptional(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)

	without := tokenizer.Encode("refused", EncodeOptions{})
	with := tokenizer.Encode("refused", EncodeOptions{AddSpecialTokens: true})

	if len(without) != 1 || without[0] != 5 {
		t.Fatalf("Encode(no specials) = %v, want [5]", without)
	}
	if len(with) != 3 || with[0] != 2 || with[1] != 5 || with[2] != 3 {
		t.Fatalf("Encode(with specials) = %v, want [2 5 3]", with)
	}
}

// Truncation must leave room for [SEP], or a long document silently loses its
// terminator and the model sees a malformed sequence.
func TestWordPieceTruncationReservesRoomForSpecialTokens(t *testing.T) {
	t.Parallel()

	tokenizer := testTokenizer(t)
	long := strings.Repeat("refused ", 50)

	got := tokenizer.Encode(long, EncodeOptions{AddSpecialTokens: true, MaxTokens: 10})
	if len(got) != 10 {
		t.Fatalf("Encode() length = %d, want exactly 10", len(got))
	}
	if got[0] != 2 {
		t.Fatalf("Encode() first token = %d, want [CLS] 2", got[0])
	}
	if got[len(got)-1] != 3 {
		t.Fatalf("Encode() last token = %d, want [SEP] 3", got[len(got)-1])
	}
}

func TestLoadWordPieceRejectsVocabWithoutUNK(t *testing.T) {
	t.Parallel()

	path := writeVocab(t, []string{"[PAD]", "hello"})
	if _, err := LoadWordPiece(path, true); err == nil {
		t.Fatal("LoadWordPiece() error = nil, want a missing-[UNK] error")
	}
}

func TestLoadWordPieceRejectsMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := LoadWordPiece(filepath.Join(t.TempDir(), "nope.txt"), true); err == nil {
		t.Fatal("LoadWordPiece() error = nil, want a file error")
	}
}
