package speech

import (
	"reflect"
	"testing"
)

func TestSplitKeepsOffsetsOfEachSentence(t *testing.T) {
	got := Split("Ciao. Come stai?  Bene!\n\nFine", 0)
	want := []Sentence{{Text: "Ciao.", Offset: 0}, {Text: "Come stai?", Offset: 6}, {Text: "Bene!", Offset: 18}, {Text: "Fine", Offset: 25}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestSplitDoesNotBreakNumbersOrLongRuns(t *testing.T) {
	if got := Split("Costa 3.50 euro.", 0); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	got := Split("uno due tre quattro cinque", 8)
	if len(got) < 3 || got[0].Text != "uno due" || got[1].Offset != 8 {
		t.Fatalf("%+v", got)
	}
}

func TestPiecesPlaceMarksBetweenSentences(t *testing.T) {
	got := Pieces(`<speak>Primo<mark name="a"/> secondo &amp; terzo.<mark name='b'/></speak>`, 0)
	if len(got) != 4 || got[1].Mark != "a" || got[3].Mark != "b" || got[2].Text != "secondo & terzo." {
		t.Fatalf("%+v", got)
	}
}

func TestStripSSMLLeavesPlainTextAlone(t *testing.T) {
	if StripSSML("a < b") != "a < b" && StripSSML("plain") != "plain" {
		t.Fatal("plain text changed")
	}
	if got := StripSSML("<speak>x &lt; y</speak>"); got != " x < y " {
		t.Fatalf("%q", got)
	}
}
